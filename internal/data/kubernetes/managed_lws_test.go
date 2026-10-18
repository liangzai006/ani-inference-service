package kubernetes

import (
	"strings"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func managedLWSFixture(t *testing.T) (RuntimeSpec, *lwsv1.LeaderWorkerSet) {
	t.Helper()
	const id = "10000000-0000-4000-8000-000000000001"
	r := &gpu.Request{ClusterID: id, PoolID: id, ProfileID: id, ProfileVersion: 1, Replicas: 2, DevicesPerReplica: 1, ContainerName: "main"}
	snapshot := &gpu.Snapshot{Request: r, Execution: &gpu.Execution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "gpu-runtime", NodeSelector: map[string]string{"accelerator.ani.io/model-key": "nvidia-test"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-cores": "25", "volcano.sh/vgpu-number": "1"}, ExecutionDigest: strings.Repeat("a", 64)}, Display: &gpu.Display{ProfileID: id, ProfileVersion: 1, ProfileName: "test", ModelKey: "nvidia-test", Mode: 2}, ExecutionDigest: strings.Repeat("a", 64)}
	spec := RuntimeSpec{TenantID: id, ServiceID: id, Namespace: "models", Name: "distributed", Image: "engine:v1", Generation: 1, RuntimeMode: "leader_worker_set", Replicas: 1, WorkerReplicas: 1, ManagedGPU: true, GPUSnapshot: snapshot, Resources: resources.Normalized{GPU: r}}
	template := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"keep": "value"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "engine:v1", Command: []string{"engine"}, Args: []string{"serve", "--caller-flag"}}}}}
	input := &lwsv1.LeaderWorkerSet{ObjectMeta: metav1.ObjectMeta{Name: KServeLLMWorkloadName(spec.Name), Namespace: spec.Namespace}, Spec: lwsv1.LeaderWorkerSetSpec{Replicas: ptr.To[int32](1), LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{Size: ptr.To[int32](2), LeaderTemplate: template.DeepCopy(), WorkerTemplate: *template.DeepCopy()}}}
	return spec, input
}

func TestManagedLWSPreCreateProjection(t *testing.T) {
	spec, input := managedLWSFixture(t)
	before := input.DeepCopy()
	if _, err := ProjectManagedGPULeaderWorkerSet(spec, input); err == nil || !strings.Contains(err.Error(), "explicitly unsupported") {
		t.Fatalf("GPU LWS was not rejected: %v", err)
	}
	if input.ResourceVersion != before.ResourceVersion || input.Name != before.Name {
		t.Fatal("rejection mutated caller object")
	}
}

func TestManagedLWSPreCreateRejectsConflicts(t *testing.T) {
	for _, name := range []string{"already created", "different namespace", "groups", "group size", "missing leader", "worker container", "extra sidecar", "GPU init container", "unplanned device", "queue", "pod annotation", "scheduler"} {
		t.Run(name, func(t *testing.T) {
			spec, input := managedLWSFixture(t)
			if _, err := ProjectManagedGPULeaderWorkerSet(spec, input); err == nil || !strings.Contains(err.Error(), "explicitly unsupported") {
				t.Fatalf("conflicting GPU LWS accepted: %v", err)
			}
		})
	}
}
