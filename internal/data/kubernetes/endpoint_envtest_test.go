package kubernetes

import (
	"context"
	"errors"
	"testing"

	bizinference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Called by the isolated envtest harness. Only Kubernetes is real here;
// the desired state and durable bindings are supplied by test fixtures.
func testEndpointDeletionAPI(t *testing.T, ctx context.Context, kube client.Client, namespace string) {
	t.Helper()
	spec := DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "endpoint-tenant", ServiceID: "endpoint-service", Name: "endpoint-delete", Namespace: namespace,
		Image: "engine:v1", Generation: 1, Replicas: 1,
		Endpoint: &EndpointSpec{ContainerPort: 9000, ServicePort: 80, TargetPort: intstr.FromInt(9000), Protocol: corev1.ProtocolTCP},
	}, DesiredState: "stopped"}
	bindings := &recordingBindingStore{}
	e := &RuntimeExecutor{Client: kube, APIReader: kube, Bindings: bindings}
	obj, err := e.applyEndpointFenced(ctx, spec.RuntimeSpec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings.records) != 1 {
		t.Fatalf("endpoint apply did not record binding: %+v", bindings.records)
	}
	r := bindings.records[0]
	binding := RuntimeBinding{Kind: r.Kind, Role: r.Role, Name: r.Name, Namespace: r.Namespace, UID: r.UID, ResourceVersion: r.ResourceVersion, Generation: r.Generation}
	if binding.Kind != "Service" || binding.Role != "endpoint" || binding.Name != spec.Name+"-endpoint" || binding.UID == "" || binding.ResourceVersion == "" {
		t.Fatalf("invalid API binding: %+v", binding)
	}
	spec.Bindings = []RuntimeBinding{binding}
	e.Source = fakeRuntimeSource{desired: spec}
	op := bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, LeaseToken: "lease", Kind: "stop"}
	if err := e.DeleteRuntime(ctx, op); err == nil {
		t.Fatal("delete accepted without confirmed publication withdrawal")
	}
	current := &corev1.Service{}
	key := client.ObjectKeyFromObject(obj)
	if err := kube.Get(ctx, key, current); err != nil {
		t.Fatalf("publication gate did not preserve endpoint: %v", err)
	}
	// Hold deletion pending to prove a successful DELETE is not absence.
	current.Finalizers = []string{"ani.kubercloud.com/test-deletion-hold"}
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(ctx, binding); !apierrors.IsConflict(err) {
		t.Fatalf("stale resourceVersion delete = %v, want Conflict", err)
	}
	fact, missing, err := e.observeBinding(ctx, spec.TenantID, spec.ServiceID, binding)
	if err != nil || missing || fact.ResourceVersion != current.ResourceVersion || fact.ExpectedResourceVersion != binding.ResourceVersion {
		t.Fatalf("observation lost current/expected RV: fact=%+v missing=%v err=%v", fact, missing, err)
	}
	spec.Bindings[0].ResourceVersion = fact.ResourceVersion
	spec.PublicationWithdrawn = true
	e.Source = fakeRuntimeSource{desired: spec}
	if err := e.DeleteRuntime(ctx, op); err != nil {
		t.Fatal(err)
	}
	observation, err := e.ObserveAbsence(ctx, op)
	if err != nil || observation.Absent || observation.RuntimePhase != "degraded" {
		t.Fatalf("terminating endpoint incorrectly absent: %+v err=%v", observation, err)
	}
	if err := kube.Get(ctx, key, current); err != nil {
		t.Fatal(err)
	}
	if current.DeletionTimestamp == nil {
		t.Fatal("DELETE did not start endpoint termination")
	}
	current.Finalizers = nil
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	observation, err = e.ObserveAbsence(ctx, op)
	if err != nil || !observation.Absent || len(observation.Objects) != 1 || !observation.Objects[0].Missing {
		t.Fatalf("deleted endpoint absence not confirmed: %+v err=%v", observation, err)
	}
	if err := e.DeleteRuntime(ctx, op); err != nil {
		t.Fatalf("absent endpoint delete retry failed: %v", err)
	}
	replacement, err := Service(spec.RuntimeSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if replacement.UID == obj.GetUID() {
		t.Fatal("recreated endpoint retained old UID")
	}
	if err := e.DeleteRuntime(ctx, op); !apierrors.IsConflict(err) {
		t.Fatalf("old binding delete of replacement = %v, want Conflict", err)
	}
	if _, err := e.ObserveAbsence(ctx, op); !errors.Is(err, bizreconcile.ErrStaleGeneration) {
		t.Fatalf("replacement accepted as old endpoint: %v", err)
	}
	if err := kube.Get(ctx, key, current); err != nil || current.UID != replacement.UID || current.DeletionTimestamp != nil {
		t.Fatalf("replacement was removed: %+v err=%v", current, err)
	}
}
