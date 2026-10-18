package accelerator

import (
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"testing"
)

func TestExecutionAdapterPreservesOpaqueMaps(t *testing.T) {
	r := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	execution := &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "q", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "group", "accelerator.ani.io/model-key": "m", "accelerator.ani.io/baseline-id": "baseline"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2", "volcano.sh/vgpu-cores": "25"}}
	execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(execution)
	in := &acceleratorv1.ResolvedGpuRequest{Execution: execution, Selection: &acceleratorv1.GpuDisplay{ProfileId: r.ProfileID, ProfileVersion: 1, ProfileName: "p", ModelKey: "m", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}}
	got, err := fromProtoSnapshot(in, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution.Resources["volcano.sh/vgpu-memory"] != "2" || got.Execution.PodAnnotations["volcano.sh/vgpu-mode"] != "hami-core" {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestExecutionAdapterRejectsMalformedExecution(t *testing.T) {
	r := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	in := &acceleratorv1.ResolvedGpuRequest{Execution: &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "q", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"bad": "selector"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2", "volcano.sh/vgpu-cores": "25"}}, Selection: &acceleratorv1.GpuDisplay{ProfileId: r.ProfileID, ProfileVersion: 1, ProfileName: "p", ModelKey: "m", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}}
	if _, err := fromProtoSnapshot(in, r); err == nil {
		t.Fatal("malformed accelerator execution accepted")
	}
}
