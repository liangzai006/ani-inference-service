package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type diagnosticSource struct{ value DesiredRuntime }

func (s diagnosticSource) CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error) {
	return s.value, nil
}
func diagnosticFixture(t *testing.T) (*RuntimeDiagnostics, biz.Scope) {
	t.Helper()
	scope := biz.Scope{TenantID: "tenant-a", ServiceID: "svc-a", Generation: 1}
	owner := true
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "runtime", Name: "runtime", UID: "deployment-uid"}}
	replicaSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "runtime", Name: "replica", UID: "replica-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "runtime", UID: deployment.UID, Controller: &owner}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "runtime", Name: "owned", UID: "pod-uid", Labels: map[string]string{tenantIDLabel: scope.TenantID, serviceIDLabel: scope.ServiceID}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "replica", UID: replicaSet.UID, Controller: &owner}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine"}}}}
	foreign := pod.DeepCopy()
	foreign.Name = "foreign"
	foreign.UID = "foreign-uid"
	foreign.OwnerReferences = nil
	replaced := pod.DeepCopy()
	replaced.Name = "replaced-owner"
	replaced.UID = "replaced-uid"
	replaced.OwnerReferences[0].UID = "stale-replica-uid"
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := kubefake.NewSimpleClientset(pod, foreign, replaced)
	return &RuntimeDiagnostics{Source: diagnosticSource{DesiredRuntime{RuntimeSpec: RuntimeSpec{Namespace: "runtime", TenantID: scope.TenantID, ServiceID: scope.ServiceID, Generation: scope.Generation}, Bindings: []RuntimeBinding{{Namespace: "runtime", Kind: "Deployment", Name: deployment.Name, UID: string(deployment.UID)}}}}, Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, replicaSet).Build(), Core: client.CoreV1()}, scope
}
func TestDiagnosticsRequiresUIDAncestryNotOnlyLabels(t *testing.T) {
	d, scope := diagnosticFixture(t)
	instances, err := d.Instances(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].Name != "owned" {
		t.Fatalf("foreign or replaced objects leaked: %+v", instances)
	}
	scope.TenantID = "tenant-b"
	if _, err := d.Instances(context.Background(), scope); err == nil {
		t.Fatal("cross tenant runtime source must fail closed")
	}
}
func TestDiagnosticEventsFilterBeforeTotalAndPage(t *testing.T) {
	d, scope := diagnosticFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		typ := "Warning"
		if i == 0 {
			typ = "Normal"
		}
		uid := types.UID("pod-uid")
		if i == 4 {
			uid = "foreign-uid"
		}
		_, err := d.Core.Events("runtime").Create(ctx, &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("event-%d", i), Namespace: "runtime", UID: types.UID(fmt.Sprintf("event-%d", i))}, InvolvedObject: corev1.ObjectReference{Name: "owned", UID: uid}, Type: typ, Reason: "BackOff", LastTimestamp: metav1.NewTime(now.Add(time.Duration(i) * time.Minute))}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := d.Events(ctx, scope, biz.EventsQuery{Type: "Warning", Since: now.Add(time.Minute), Until: now.Add(4 * time.Minute), Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 1 || items[0].ID != "event-2" {
		t.Fatalf("items=%+v total=%d", items, total)
	}
}
func TestRuntimeLogsTimeWindowBoundsAndCancellation(t *testing.T) {
	since := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
	input := "2026-10-21T00:59:59Z old\n2026-10-21T01:00:00Z first\n2026-10-21T01:00:01Z second\n2026-10-21T01:00:02Z after\n"
	var lines []biz.LogLine
	truncated, err := readRuntimeLogs(context.Background(), strings.NewReader(input), biz.LogsQuery{Since: since, Until: since.Add(2 * time.Second), TailLines: 20}, func(line biz.LogLine) error { lines = append(lines, line); return nil })
	if err != nil || truncated || len(lines) != 2 {
		t.Fatalf("lines=%+v truncated=%v err=%v", lines, truncated, err)
	}
	truncated, err = readRuntimeLogs(context.Background(), strings.NewReader(input), biz.LogsQuery{TailLines: 1}, func(biz.LogLine) error { return nil })
	if err != nil || !truncated {
		t.Fatalf("expected truncation: %v %v", truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = readRuntimeLogs(ctx, strings.NewReader(input), biz.LogsQuery{TailLines: 20}, func(biz.LogLine) error { t.Fatal("canceled reader emitted a line"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	sentinel := errors.New("consumer canceled")
	_, err = readRuntimeLogs(context.Background(), strings.NewReader(input), biz.LogsQuery{TailLines: 20}, func(biz.LogLine) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("consumer failure lost: %v", err)
	}
}

func TestKServeBindingKindMatchesAndUnlabelledWorkersAreIncluded(t *testing.T) {
	d, scope := diagnosticFixture(t)
	pod, err := d.Core.Pods("runtime").Get(context.Background(), "owned", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Labels = nil
	if _, err = d.Core.Pods("runtime").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	instances, err := d.Instances(context.Background(), scope)
	if err != nil || len(instances) != 1 {
		t.Fatalf("unlabelled owned worker missing: %+v %v", instances, err)
	}
	if diagnosticBindingKind(KServeInferenceServiceKind) != "InferenceService" || diagnosticBindingKind(KServeLLMInferenceServiceKind) != "LLMInferenceService" {
		t.Fatal("KServe binding kinds mismatch")
	}
	if diagnosticOwnerKind("untrusted.example/v1", "InferenceService") {
		t.Fatal("unknown resource group accepted")
	}
}
