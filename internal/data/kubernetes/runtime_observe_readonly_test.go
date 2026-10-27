package kubernetes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	bizreconcile "github.com/liangzai006/ani-inference-service/internal/biz/reconcile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Reject every Kubernetes mutation at the external boundary. The underlying
// fake client still serves real object reads and controller status changes.
type readOnlyRuntimeClient struct {
	client.Client
	writes int
}

func (c *readOnlyRuntimeClient) rejectWrite() error {
	c.writes++
	return errors.New("read-only runtime observation attempted a Kubernetes write")
}

func (c *readOnlyRuntimeClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return c.rejectWrite()
}

func (c *readOnlyRuntimeClient) Update(context.Context, client.Object, ...client.UpdateOption) error {
	return c.rejectWrite()
}

func (c *readOnlyRuntimeClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return c.rejectWrite()
}

func (c *readOnlyRuntimeClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return c.rejectWrite()
}

func (c *readOnlyRuntimeClient) DeleteAllOf(context.Context, client.Object, ...client.DeleteAllOfOption) error {
	return c.rejectWrite()
}

func readOnlyRuntimeFixture(t *testing.T, provider string) (DesiredRuntime, client.Object, *appsv1.Deployment, []client.Object) {
	t.Helper()
	spec := DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Generation: 2, Namespace: "models", Name: "model", Image: "engine:v2", Replicas: 1, RuntimeProvider: provider},
		DesiredState: "running", ModelReady: true, ModelReadyKnown: true,
	}
	dep, err := Deployment(spec.RuntimeSpec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion, dep.Generation = "runtime-uid", "12", 2
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	var obj client.Object = dep
	kind := "Deployment"
	objects := []client.Object{dep}
	if provider == "kserve" {
		isvc, err := renderKServeInferenceService(spec.RuntimeSpec)
		if err != nil {
			t.Fatal(err)
		}
		isvc.SetUID("runtime-uid")
		isvc.SetResourceVersion("12")
		isvc.SetGeneration(2)
		isvc.Object["status"] = map[string]interface{}{"observedGeneration": int64(2), "conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}}
		controller := true
		dep.Name, dep.UID = KServePredictorServiceName(spec.Name), "predictor-uid"
		dep.OwnerReferences = []metav1.OwnerReference{{APIVersion: isvc.GetAPIVersion(), Kind: isvc.GetKind(), Name: isvc.GetName(), UID: isvc.GetUID(), Controller: &controller}}
		containers, _, _ := unstructured.NestedSlice(isvc.Object, "spec", "predictor", "containers")
		var container corev1.Container
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(containers[0].(map[string]interface{}), &container); err != nil {
			t.Fatal(err)
		}
		dep.Spec.Template.Spec.Containers = []corev1.Container{container}
		dep.Spec.Template.Spec.SchedulerName = "volcano"
		dep.Spec.Template.Annotations = map[string]string{"scheduling.volcano.sh/queue-name": "default"}
		obj, kind = isvc, KServeInferenceServiceKind
		objects = append(objects, isvc)
	}
	// Controller status updates advance RV without changing durable ownership.
	// Reusing the existing observation fence must preserve the expected old RV.
	spec.Bindings = []RuntimeBinding{{Generation: 2, Kind: kind, Role: "runtime", Namespace: spec.Namespace, Name: spec.Name, UID: "runtime-uid", ResourceVersion: "11"}}
	return spec, obj, dep, objects
}

func readOnlyRuntimeObserver(provider string, cl client.Client, spec DesiredRuntime) func(context.Context, DesiredRuntime) (bizreconcile.Observation, error) {
	executor := &RuntimeExecutor{Client: cl, APIReader: cl, Source: fakeRuntimeSource{desired: spec}}
	if provider == "kserve" {
		return (&KServeRuntimeExecutor{RuntimeExecutor: executor}).observeCurrent
	}
	return executor.observeCurrent
}

func TestRuntimeObserveCurrentReadsReadinessRegressionWithoutMutation(t *testing.T) {
	for _, provider := range []string{"deployment", "kserve"} {
		t.Run(provider, func(t *testing.T) {
			spec, _, dep, objects := readOnlyRuntimeFixture(t, provider)
			base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).WithStatusSubresource(dep).Build()
			cl := &readOnlyRuntimeClient{Client: base}
			observe := readOnlyRuntimeObserver(provider, cl, spec)
			got, err := observe(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if got.RuntimePhase != "ready" || got.ReadyReplicas != 1 || got.Generation != 2 || !got.ModelReady || !got.ModelReadyKnown || len(got.Objects) != 1 {
				t.Fatalf("current ready runtime = %+v", got)
			}
			if got.Objects[0].UID != "runtime-uid" || got.Objects[0].ResourceVersion != "12" || got.Objects[0].ExpectedResourceVersion != "11" {
				t.Fatalf("read-only observation lost the durable identity/RV fence: %+v", got.Objects)
			}
			current := &appsv1.Deployment{}
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(dep), current); err != nil {
				t.Fatal(err)
			}
			current.Status.ReadyReplicas, current.Status.AvailableReplicas = 0, 0
			if err := base.Status().Update(context.Background(), current); err != nil {
				t.Fatal(err)
			}
			got, err = observe(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if got.RuntimePhase == "ready" || got.Reason == "" || got.InvocationHealth != "unhealthy" {
				t.Fatalf("controller readiness regression was hidden: %+v", got)
			}
			if cl.writes != 0 {
				t.Fatalf("read-only observation made %d Kubernetes writes", cl.writes)
			}
		})
	}
}

func TestRuntimeObserveCurrentReportsMissingWithoutRepairOrAdoption(t *testing.T) {
	for _, provider := range []string{"deployment", "kserve"} {
		for _, bound := range []bool{true, false} {
			name := provider + "/unbound"
			if bound {
				name = provider + "/bound"
			}
			t.Run(name, func(t *testing.T) {
				spec, obj, _, _ := readOnlyRuntimeFixture(t, provider)
				if !bound {
					spec.Bindings = nil
				}
				base := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
				cl := &readOnlyRuntimeClient{Client: base}
				observe := readOnlyRuntimeObserver(provider, cl, spec)
				got, err := observe(context.Background(), spec)
				if err != nil || got.RuntimePhase != "degraded" || got.Reason == "" || got.InvocationHealth != "unhealthy" {
					t.Fatalf("missing object = %+v, %v", got, err)
				}
				if bound && provider == "deployment" && (len(got.Objects) != 1 || !got.Objects[0].Missing || got.Objects[0].ExpectedUID != "runtime-uid") {
					t.Fatalf("missing bound runtime lost absence evidence: %+v", got)
				}
				obj.SetResourceVersion("")
				if err := base.Create(context.Background(), obj); err != nil {
					t.Fatal(err)
				}
				if !bound {
					if _, err := observe(context.Background(), spec); !errors.Is(err, bizreconcile.ErrStaleGeneration) {
						t.Fatalf("existing unbound runtime was adopted: %v", err)
					}
				}
				if cl.writes != 0 {
					t.Fatalf("read-only observation made %d Kubernetes writes", cl.writes)
				}
			})
		}
	}
}

func TestRuntimeObserveCurrentRetainsInvocationHealthWithoutMutation(t *testing.T) {
	for _, provider := range []string{"deployment", "kserve"} {
		t.Run(provider, func(t *testing.T) {
			var healthy atomic.Bool
			healthy.Store(true)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/completions" {
					t.Errorf("probe request = %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if healthy.Load() {
					_, _ = w.Write([]byte(`{"choices":[{"text":"ok"}]}`))
				} else {
					_, _ = w.Write([]byte(`{"choices":[]}`))
				}
			}))
			defer endpoint.Close()
			u, err := url.Parse(endpoint.URL)
			if err != nil {
				t.Fatal(err)
			}
			host, port, err := net.SplitHostPort(u.Host)
			if err != nil {
				t.Fatal(err)
			}
			portNumber, err := strconv.Atoi(port)
			if err != nil {
				t.Fatal(err)
			}
			spec, _, dep, objects := readOnlyRuntimeFixture(t, provider)
			spec.PublicationPublished, spec.ServedModelName = true, "model-a"
			spec.Endpoint = &EndpointSpec{ContainerPort: int32(portNumber), ServicePort: 80, TargetPort: intstr.FromInt(portNumber), Protocol: corev1.ProtocolTCP}
			service, err := Service(spec.RuntimeSpec)
			if err != nil {
				t.Fatal(err)
			}
			service.UID, service.ResourceVersion = "endpoint-uid", "13"
			objects = append(objects, service)
			spec.Bindings = append(spec.Bindings, RuntimeBinding{Generation: 2, Kind: "Service", Role: "endpoint", Namespace: spec.Namespace, Name: service.Name, UID: string(service.UID), ResourceVersion: service.ResourceVersion})
			controller := true
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: spec.Namespace, Name: "runtime-pod", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: dep.Name, UID: dep.UID, Controller: &controller}}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "engine"}}},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: host, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
			}
			core := kubefake.NewSimpleClientset(pod)
			cl := &readOnlyRuntimeClient{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()}
			executor := &RuntimeExecutor{Client: cl, APIReader: cl, Core: core.CoreV1(), Source: fakeRuntimeSource{desired: spec}}
			observe := executor.observeCurrent
			if provider == "kserve" {
				observe = (&KServeRuntimeExecutor{RuntimeExecutor: executor}).observeCurrent
			}
			for _, want := range []string{"healthy", "unhealthy"} {
				got, err := observe(context.Background(), spec)
				if err != nil || got.RuntimePhase != "ready" || got.InvocationHealth != want {
					t.Fatalf("current invocation health = %+v, %v; want %s", got, err, want)
				}
				healthy.Store(false)
			}
			if cl.writes != 0 {
				t.Fatalf("read-only observation made %d Kubernetes writes", cl.writes)
			}
			for _, action := range core.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("invocation observation made Kubernetes %s", action.GetVerb())
				}
			}
		})
	}
}

func TestRuntimeObserveCurrentPreservesOwnershipAndGenerationFences(t *testing.T) {
	for _, provider := range []string{"deployment", "kserve"} {
		for _, tc := range []struct {
			name   string
			change func(*DesiredRuntime, client.Object)
		}{
			{name: "other tenant", change: func(_ *DesiredRuntime, obj client.Object) {
				labels := obj.GetLabels()
				labels[tenantIDLabel] = "tenant-b"
				obj.SetLabels(labels)
			}},
			{name: "other service", change: func(_ *DesiredRuntime, obj client.Object) {
				labels := obj.GetLabels()
				labels[serviceIDLabel] = "service-b"
				obj.SetLabels(labels)
			}},
			{name: "other generation", change: func(_ *DesiredRuntime, obj client.Object) {
				labels := obj.GetLabels()
				labels[generationLabel] = "3"
				obj.SetLabels(labels)
			}},
			{name: "replacement UID", change: func(_ *DesiredRuntime, obj client.Object) { obj.SetUID(types.UID("replacement")) }},
			{name: "different bound name", change: func(spec *DesiredRuntime, _ client.Object) { spec.Bindings[0].Name = "other" }},
			{name: "different bound namespace", change: func(spec *DesiredRuntime, _ client.Object) { spec.Bindings[0].Namespace = "other" }},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				spec, obj, _, objects := readOnlyRuntimeFixture(t, provider)
				tc.change(&spec, obj)
				cl := &readOnlyRuntimeClient{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()}
				if _, err := readOnlyRuntimeObserver(provider, cl, spec)(context.Background(), spec); err == nil {
					t.Fatal("read-only observation accepted an ownership or generation mismatch")
				}
				if cl.writes != 0 {
					t.Fatalf("rejected observation made %d Kubernetes writes", cl.writes)
				}
			})
		}
	}
}
