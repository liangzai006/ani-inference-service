package kubernetes

import (
	"context"
	"errors"
	"testing"

	bizinference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type invocationProbeFunc func(context.Context, RuntimeSpec) (InvocationResult, error)

func (f invocationProbeFunc) Probe(ctx context.Context, spec RuntimeSpec) (InvocationResult, error) {
	return f(ctx, spec)
}

func invocationExecutor(t *testing.T) (*RuntimeExecutor, DesiredRuntime, bizinference.OperationContext) {
	t.Helper()
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "inference", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion, dep.Generation = "runtime-uid", "7", 2
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1}
	desired := DesiredRuntime{RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true, ModelReady: true, ModelReadyKnown: true,
		Bindings: []RuntimeBinding{{Generation: 2, Kind: "Deployment", Namespace: "ns", Name: spec.Name, UID: "runtime-uid", ResourceVersion: "7", Role: "runtime"}}}
	e := &RuntimeExecutor{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build(), Source: fakeRuntimeSource{desired: desired}}
	op := bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "lease"}
	return e, desired, op
}

func TestInvocationObservationRequiresCurrentPublishedGeneration(t *testing.T) {
	for _, p := range []publication.Publication{{}, {State: "publishing", Generation: 2}, {State: "published", Generation: 1}, {State: "published", Generation: 2}} {
		e, _, op := invocationExecutor(t)
		op.Publication = p
		calls := 0
		e.Invocation = invocationProbeFunc(func(context.Context, RuntimeSpec) (InvocationResult, error) {
			calls++
			return InvocationResult{Known: true, Healthy: true}, nil
		})
		observation, err := e.ObserveRuntime(context.Background(), op)
		if err != nil {
			t.Fatal(err)
		}
		want := p.State == "published" && p.Generation == 2
		if (calls == 1) != want || observation.InvocationKnown != want || observation.InvocationHealthy != want {
			t.Fatalf("publication=%+v calls=%d observation=%+v, want probe=%v", p, calls, observation, want)
		}
	}
}

func TestInvocationProbeErrorReturnsUnknownObservationForPersistence(t *testing.T) {
	e, _, op := invocationExecutor(t)
	op.Publication = publication.Publication{State: "published", Generation: 2}
	e.Invocation = invocationProbeFunc(func(context.Context, RuntimeSpec) (InvocationResult, error) {
		return InvocationResult{}, errors.New("probe failed")
	})
	observation, err := e.ObserveRuntime(context.Background(), op)
	if err != nil || observation.InvocationKnown || observation.InvocationHealthy || observation.Reason == "" {
		t.Fatalf("failed probe must persist unknown instead of leaving old healthy: observation=%+v err=%v", observation, err)
	}
}

func TestContinuousObservationRefreshesInvocationHealth(t *testing.T) {
	for _, tt := range []struct {
		name       string
		published  bool
		result     InvocationResult
		probeError error
		want       string
	}{
		{"published healthy", true, InvocationResult{Known: true, Healthy: true}, nil, "healthy"},
		{"published unhealthy", true, InvocationResult{Known: true}, nil, "unhealthy"},
		{"published unknown", true, InvocationResult{}, nil, "unknown"},
		{"published probe error", true, InvocationResult{}, errors.New("probe failed"), "unknown"},
		{"not published", false, InvocationResult{Known: true, Healthy: true}, nil, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, desired, _ := invocationExecutor(t)
			desired.PublicationPublished = tt.published
			e.Source = fakeRuntimeSource{desired: desired}
			calls := 0
			e.Invocation = invocationProbeFunc(func(context.Context, RuntimeSpec) (InvocationResult, error) { calls++; return tt.result, tt.probeError })
			observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: desired.TenantID, ServiceID: desired.ServiceID, Generation: desired.Generation})
			if err != nil || observation.InvocationHealth != tt.want || !observation.ModelReadyKnown || !observation.ModelReady || (calls == 1) != tt.published {
				t.Fatalf("continuous observation=%+v calls=%d err=%v, want health=%s", observation, calls, err, tt.want)
			}
		})
	}
}

func TestEndpointTerminatingIsDegradedBeforeAbsence(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "inference", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1,
		Endpoint: &EndpointSpec{ContainerPort: 9000, ServicePort: 80, TargetPort: intstr.FromInt(9000), Protocol: corev1.ProtocolTCP}}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1}
	service, err := Service(spec)
	if err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	service.DeletionTimestamp = &now
	service.Finalizers = []string{"test.ani.kubercloud.com/hold"}
	desired := DesiredRuntime{RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Generation: 2, Kind: "Deployment", Role: "runtime", Namespace: "ns", Name: spec.Name, UID: "dep", ResourceVersion: "1"},
			{Generation: 2, Kind: "Service", Role: "endpoint", Namespace: "ns", Name: service.Name, UID: "svc", ResourceVersion: "1"}}}
	dep.UID, dep.ResourceVersion = "dep", "1"
	service.UID, service.ResourceVersion = "svc", "1"
	e := &RuntimeExecutor{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep, service).Build(), Source: fakeRuntimeSource{desired: desired}}
	observation, err := e.observeHealth(context.Background(), desired, &desired.Bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "degraded" || observation.Reason != "endpoint service is being deleted" {
		t.Fatalf("terminating endpoint was treated as ready: %+v", observation)
	}
}
