package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"testing"
)

func TestModelRuntimeMountsVerifiedClaimAndProbesEngine(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "demo", Namespace: "ns", Image: "engine:fixed", Generation: 1, Replicas: 1, EngineRuntime: "custom-engine", ArtifactProvider: "model", Endpoint: &EndpointSpec{ContainerPort: 8000, ServicePort: 80, TargetPort: intstr.FromInt(8000), Protocol: corev1.ProtocolTCP}}
	spec.ModelClaim = ModelClaimName(spec)
	obj, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	c := obj.Spec.Template.Spec.Containers[0]
	if len(c.VolumeMounts) == 0 || c.VolumeMounts[0].MountPath != "/models" || !c.VolumeMounts[0].ReadOnly || c.VolumeMounts[0].SubPath != "data" {
		t.Fatal("verified model volume is not mounted read-only")
	}
	if c.ReadinessProbe == nil || c.StartupProbe == nil || c.ReadinessProbe.HTTPGet.Path != "/health" {
		t.Fatal("engine readiness must be observed")
	}
	next := spec
	next.Generation = 2
	if ModelClaimName(next) == ModelClaimName(spec) {
		t.Fatal("generations share materialization volume")
	}
}

func TestModelRuntimeMountsOnLeaderWorkerSet(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "distributed", Namespace: "ns", Image: "engine:fixed", Generation: 1, Replicas: 1, WorkerReplicas: 1, RuntimeMode: "leader_worker_set", EngineRuntime: "custom-engine", ArtifactProvider: "model", ModelClaim: "claim", ContainerPort: 8000, ServiceProtocol: corev1.ProtocolTCP}
	obj, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	container := obj.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
	if len(container.VolumeMounts) != 2 || container.VolumeMounts[0].MountPath != "/models" {
		t.Fatalf("model claim was not mounted on worker template: %+v", container.VolumeMounts)
	}
}
