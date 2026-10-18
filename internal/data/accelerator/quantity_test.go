package accelerator

import (
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"testing"
)

func TestExecutionAdapterPreservesOpaqueMaps(t *testing.T) {
	r := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	in := &acceleratorv1.ResolvedGpuRequest{Execution: &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "q", NodeSelector: map[string]string{"a": "b"}, PodAnnotations: map[string]string{"x": "y"}, Resources: map[string]string{"vendor.com/gpu": "2"}, ExecutionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Selection: &acceleratorv1.GpuDisplay{ProfileId: r.ProfileID, ProfileVersion: 1, ProfileName: "p", ModelKey: "m", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}}
	got, err := fromProtoSnapshot(in, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution.Resources["vendor.com/gpu"] != "2" || got.Execution.PodAnnotations["x"] != "y" {
		t.Fatalf("snapshot=%+v", got)
	}
}
