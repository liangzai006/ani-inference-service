package kubernetes

import (
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestDeploymentRendersNativeResourceMaps(t *testing.T) {
	normalized, err := resources.Normalize(resources.Spec{
		Requests: map[string]string{"cpu": "2", "nvidia.com/gpu": "1"},
		Limits:   map[string]string{"cpu": "4", "nvidia.com/gpu": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var obj runtime.Object
	obj, err = Deployment(RuntimeSpec{TenantID: "t1", ServiceID: "s1", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 1, Resources: normalized})
	if err != nil {
		t.Fatal(err)
	}
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		t.Fatalf("Deployment() returned %T, want *appsv1.Deployment", obj)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v, want 1", deployment.Spec.Replicas)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	got := container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
	if got.String() != "1" {
		t.Fatalf("GPU limit = %q, want 1", got.String())
	}
	if deployment.Spec.Selector == nil {
		t.Fatal("generation must not be part of the immutable Deployment selector")
	}
	if _, found := deployment.Spec.Selector.MatchLabels[generationLabel]; found {
		t.Fatal("generation must not be part of the immutable Deployment selector")
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Command; len(got) != 0 {
		t.Fatalf("command = %v, want omitted for empty argv", got)
	}
}

func TestDeploymentRejectsMissingTenantAndInvalidQuantity(t *testing.T) {
	normalized := resources.Normalized{Requests: map[string]string{"cpu": "not-a-quantity"}}
	if _, err := Deployment(RuntimeSpec{TenantID: "tenant-a", ServiceID: "s1", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1, Resources: normalized}); err == nil {
		t.Fatal("Deployment accepted an invalid quantity")
	}
	valid := resources.Normalized{Requests: map[string]string{"cpu": "1"}}
	if _, err := Deployment(RuntimeSpec{ServiceID: "s1", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1, Resources: valid}); err == nil {
		t.Fatal("Deployment accepted a missing tenant")
	}
}

func TestDeploymentDoesNotSilentlyDowngradeLeaderWorkerSet(t *testing.T) {
	if _, err := Deployment(RuntimeSpec{TenantID: "tenant-a", ServiceID: "s1", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1, WorkerReplicas: 2, RuntimeMode: "leader_worker_set"}); err == nil {
		t.Fatal("LWS runtime was silently rendered as Deployment")
	}
}

func TestLeaderWorkerSetRendersGroupsAndWorkers(t *testing.T) {
	obj, err := LeaderWorkerSet(RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 3, WorkerReplicas: 4})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Spec.Replicas == nil || *obj.Spec.Replicas != 3 {
		t.Fatalf("groups = %v, want 3", obj.Spec.Replicas)
	}
	if obj.Spec.LeaderWorkerTemplate.Size == nil || *obj.Spec.LeaderWorkerTemplate.Size != 5 {
		t.Fatalf("group size = %v, want leader plus 4 workers", obj.Spec.LeaderWorkerTemplate.Size)
	}
	container := obj.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
	if container.Name != "inference" || container.Image != "engine:v1" {
		t.Fatalf("container = %#v", container)
	}
	rollout := obj.Spec.RolloutStrategy.RollingUpdateConfiguration
	if rollout == nil || rollout.MaxUnavailable.IntValue() != 1 || rollout.MaxSurge.IntValue() != 0 || rollout.Partition == nil || *rollout.Partition != 0 {
		t.Fatalf("rollout defaults = %#v, want maxUnavailable=1 maxSurge=0 partition=0", rollout)
	}
}

func TestLeaderWorkerSetRendersExtendedGPUResources(t *testing.T) {
	normalized, err := resources.Normalize(resources.Spec{
		Requests: map[string]string{"cpu": "2", "nvidia.com/gpu": "1"},
		Limits:   map[string]string{"cpu": "4", "nvidia.com/gpu": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	obj, err := LeaderWorkerSet(RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "distributed", Namespace: "ns",
		Image: "engine:v1", Generation: 1, Replicas: 2, WorkerReplicas: 2,
		RuntimeMode: "leader_worker_set", Resources: normalized,
	})
	if err != nil {
		t.Fatal(err)
	}
	container := obj.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0]
	request := container.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")]
	if got := request.String(); got != "1" {
		t.Fatalf("GPU request = %q, want 1", got)
	}
	limit := container.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]
	if got := limit.String(); got != "1" {
		t.Fatalf("GPU limit = %q, want 1", got)
	}
}

func TestServiceRendersExplicitEndpointContract(t *testing.T) {
	obj, err := Service(RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns",
		Generation: 4, ContainerPort: 8080, ServicePort: 80,
		TargetPort: intstr.FromInt(8080), ServiceProtocol: corev1.ProtocolTCP,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.Spec.Ports[0].Port != 80 || obj.Spec.Ports[0].TargetPort.IntValue() != 8080 || obj.Spec.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("service ports = %#v", obj.Spec.Ports)
	}
	if obj.Spec.Selector[generationLabel] != "4" {
		t.Fatalf("service selector = %#v, want generation selector", obj.Spec.Selector)
	}
	if _, found := obj.Spec.Selector[lwsv1.WorkerIndexLabelKey]; found {
		t.Fatal("Deployment endpoint must not require an LWS leader label")
	}
}

func TestServiceNameAvoidsLWSHeadlessService(t *testing.T) {
	spec := RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "distributed", Namespace: "ns", Image: "engine:v1",
		Generation: 1, Replicas: 2, WorkerReplicas: 3, RuntimeMode: "leader_worker_set",
		ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080), ServiceProtocol: corev1.ProtocolTCP,
	}
	lws, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := Service(spec)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Name == lws.Name {
		t.Fatal("endpoint would occupy the LWS controller's same-name headless Service")
	}
	spec.Generation, spec.RuntimeMode = 2, "deployment"
	replacement, err := Service(spec)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Name != endpoint.Name {
		t.Fatalf("endpoint name changed across generation/topology: %q -> %q", endpoint.Name, replacement.Name)
	}
}

func TestLeaderWorkerSetServiceSelectsOnlyLeaders(t *testing.T) {
	spec := RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "distributed", Namespace: "ns",
		Generation: 4, RuntimeMode: "leader_worker_set", ContainerPort: 8080, ServicePort: 80,
		TargetPort: intstr.FromInt(8080), ServiceProtocol: corev1.ProtocolTCP,
	}
	endpoint, err := Service(spec)
	if err != nil {
		t.Fatal(err)
	}
	selector := labels.SelectorFromSet(endpoint.Spec.Selector)
	podLabels := labels.Set{
		"app.kubernetes.io/name": "ani-inference", tenantIDLabel: spec.TenantID,
		serviceIDLabel: spec.ServiceID, generationLabel: "4", lwsv1.WorkerIndexLabelKey: "0",
	}
	if !selector.Matches(podLabels) {
		t.Fatal("endpoint does not select the LWS leader")
	}
	podLabels[lwsv1.WorkerIndexLabelKey] = "1"
	if selector.Matches(podLabels) {
		t.Fatal("endpoint selects an LWS worker as an inference backend")
	}
	if _, found := endpoint.Labels[lwsv1.WorkerIndexLabelKey]; found {
		t.Fatal("Service metadata must not claim to be an LWS leader Pod")
	}
}

func TestServiceRejectsInvalidEndpointName(t *testing.T) {
	for _, name := range []string{"not a DNS name", strings.Repeat("a", 63)} {
		_, err := Service(RuntimeSpec{
			TenantID: "tenant-a", ServiceID: "service-a", Name: name, Namespace: "ns", Generation: 1,
			ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080), ServiceProtocol: corev1.ProtocolTCP,
		})
		if err == nil {
			t.Fatalf("Service accepted a name that cannot form a valid endpoint name: %q", name)
		}
	}
}

func TestServiceRequiresExplicitEndpointContract(t *testing.T) {
	base := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Generation: 1}
	if _, err := Service(base); err == nil {
		t.Fatal("Service accepted an implicit endpoint")
	}
	base.ContainerPort, base.ServicePort, base.TargetPort, base.ServiceProtocol = 8080, 80, intstr.FromInt(8080), corev1.ProtocolTCP
	base.ServicePort = 0
	if _, err := Service(base); err == nil {
		t.Fatal("Service accepted an invalid service port")
	}
}

func TestOwnedByRequiresTenantAndServiceMatch(t *testing.T) {
	obj, err := Deployment(RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v1",
		Generation: 1, Replicas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !OwnedBy(obj, "tenant-a", "service-a") {
		t.Fatal("OwnedBy rejected the owning tenant and service")
	}
	if OwnedBy(obj, "tenant-b", "service-a") {
		t.Fatal("OwnedBy accepted a foreign tenant")
	}
}
