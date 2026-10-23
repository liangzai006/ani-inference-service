package kubernetes

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	bizreconcile "github.com/liangzai006/ani-inference-service/internal/biz/reconcile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestInvocationProbeUsesBoundPodAndRealGeneration(t *testing.T) {
	calls := 0
	valid := true
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || r.URL.Path != "/v1/completions" || body["model"] != "model-a" || body["max_tokens"] != float64(1) {
			t.Error("wrong probe", body)
		}
		if valid {
			w.Write([]byte(`{"choices":[{"text":"yes"}]}`))
		} else {
			w.Write([]byte(`{"choices":[]}`))
		}
	}))
	defer endpoint.Close()
	u, _ := url.Parse(endpoint.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	d, scope := diagnosticFixture(t)
	src := d.Source.(diagnosticSource)
	src.value.DesiredState = "running"
	src.value.PublicationPublished = true
	src.value.Endpoint = &EndpointSpec{ContainerPort: int32(p)}
	d.Source = src
	pod, _ := d.Core.Pods("runtime").Get(context.Background(), "owned", metav1.GetOptions{})
	pod.Status.PodIP = host
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	d.Core.Pods("runtime").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{})
	for _, want := range []string{"healthy", "unhealthy"} {
		result, err := d.ProbeInvocation(context.Background(), scope, "model-a")
		if err != nil || result.State != want || result.ObservedAt.IsZero() {
			t.Fatal(result, err)
		}
		valid = false
	}
	src.value.DesiredState = "stopped"
	d.Source = src
	result, err := d.ProbeInvocation(context.Background(), scope, "model-a")
	if err != nil || result.State != "unhealthy" || result.Reason != "endpoint_not_ready" || calls != 2 {
		t.Fatal("stopped runtime probed", result, err, calls)
	}
}

// KServe predictor Pods are owned by a ReplicaSet and Deployment which are
// themselves owned by the persisted InferenceService.  Keep this path covered
// separately from the Deployment runtime fixture: accepting a Pod merely by
// its ANI labels would make an unbound KServe workload probeable.
func TestInvocationProbeUsesKServePredictorOwnership(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/completions" {
			t.Errorf("probe request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"ok"}]}`))
	}))
	defer endpoint.Close()
	u, _ := url.Parse(endpoint.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	portNumber, _ := strconv.Atoi(port)

	scope := biz.Scope{TenantID: "tenant-a", ServiceID: "svc-a", Generation: 1}
	controller := true
	isvc := newKServeInferenceService("runtime", "predictor")
	isvc.SetUID(types.UID("isvc-uid"))
	isvc.SetResourceVersion("1")
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "runtime", Name: "predictor-predictor", UID: types.UID("deployment-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService",
				Name: "predictor", UID: isvc.GetUID(), Controller: &controller,
			}},
		},
	}
	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "runtime", Name: "predictor-predictor-rs", UID: types.UID("replica-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "Deployment", Name: deployment.Name,
				UID: deployment.UID, Controller: &controller,
			}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "runtime", Name: "predictor-predictor-pod", UID: types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet.Name,
				UID: replicaSet.UID, Controller: &controller,
			}},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "kserve-container"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning, PodIP: host,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}

	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	d := &RuntimeDiagnostics{
		Source: diagnosticSource{value: DesiredRuntime{
			RuntimeSpec: RuntimeSpec{
				TenantID: scope.TenantID, ServiceID: scope.ServiceID, Namespace: "runtime",
				Generation: scope.Generation, Endpoint: &EndpointSpec{ContainerPort: int32(portNumber)},
			},
			DesiredState: "running", PublicationPublished: true,
			Bindings: []RuntimeBinding{{
				Kind: KServeInferenceServiceKind, Role: "runtime", Namespace: "runtime",
				Name: isvc.GetName(), UID: string(isvc.GetUID()), ResourceVersion: isvc.GetResourceVersion(),
			}},
		}},
		Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(isvc, deployment, replicaSet).Build(),
		Core:   kubefake.NewSimpleClientset(pod).CoreV1(),
	}

	result, err := d.ProbeInvocation(context.Background(), scope, "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "healthy" || result.Reason != "runtime_invocation_succeeded" || result.ObservedAt.IsZero() {
		t.Fatalf("KServe predictor probe = %+v", result)
	}
}

func TestDurableObservationPersistsInvocationProbeResult(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"text":"ok"}]}`))
	}))
	defer endpoint.Close()
	u, _ := url.Parse(endpoint.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	portNumber, _ := strconv.Atoi(port)

	d, scope := diagnosticFixture(t)
	src := d.Source.(diagnosticSource)
	src.value.DesiredState = "running"
	src.value.PublicationPublished = true
	src.value.ModelReady = true
	src.value.ModelReadyKnown = true
	src.value.ServedModelName = "model-a"
	src.value.Endpoint = &EndpointSpec{ContainerPort: int32(portNumber)}
	d.Source = src
	pod, err := d.Core.Pods("runtime").Get(context.Background(), "owned", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.PodIP = host
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := d.Core.Pods("runtime").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	executor := &RuntimeExecutor{Source: d.Source, APIReader: d.Reader, Core: d.Core}
	observation := executor.observeInvocation(context.Background(), src.value, bizreconcile.Observation{
		Generation: 1, RuntimePhase: "ready", ModelReady: true, ModelReadyKnown: true,
	})
	if observation.InvocationHealth != "healthy" || observation.Reason != "runtime_invocation_succeeded" {
		t.Fatalf("durable observation = %+v", observation)
	}
	if scope.Generation != observation.Generation {
		t.Fatalf("observation generation = %d, want %d", observation.Generation, scope.Generation)
	}
}

func TestRuntimeLoadingPhaseAndHealthBeforeReady(t *testing.T) {
	if got := runtimeLoadingPhase(false, false); got != "pending" {
		t.Fatalf("unknown model readiness phase = %q, want pending", got)
	}
	if got := runtimeLoadingPhase(true, true); got != "loading" {
		t.Fatalf("ready model startup phase = %q, want loading", got)
	}
	e := &RuntimeExecutor{}
	observation := e.observeInvocation(context.Background(), DesiredRuntime{}, bizreconcile.Observation{
		RuntimePhase: "loading",
	})
	if observation.InvocationHealth != "unhealthy" || observation.Reason != "runtime_not_ready" {
		t.Fatalf("pre-ready invocation health = %+v, want unhealthy/runtime_not_ready", observation)
	}
}
