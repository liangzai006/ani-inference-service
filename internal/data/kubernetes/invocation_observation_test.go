package kubernetes

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
