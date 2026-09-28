package resources

import (
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/gpu"
	"testing"
)

func TestNormalizePreservesGpuRequestAndRejectsRawAcceleratorKey(t *testing.T) {
	g := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	n, err := Normalize(Spec{Requests: map[string]string{"cpu": "2"}, GPU: g})
	if err != nil || n.GPU != g {
		t.Fatalf("Normalize() = %#v, error=%v", n, err)
	}
	if _, err := Normalize(Spec{Requests: map[string]string{"nvidia.com/gpu": "1"}, GPU: g}); err == nil {
		t.Fatal("accepted raw accelerator key alongside gpu object")
	}
}
