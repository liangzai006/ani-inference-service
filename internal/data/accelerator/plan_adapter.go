package accelerator

import (
	"fmt"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
)

func toProtoRequest(r *gpu.Request) *acceleratorv1.GpuRequest {
	if r == nil {
		return nil
	}
	return &acceleratorv1.GpuRequest{ClusterId: r.ClusterID, PoolId: r.PoolID, ProfileId: r.ProfileID, ProfileVersion: r.ProfileVersion, Replicas: r.Replicas, DevicesPerReplica: r.DevicesPerReplica, ContainerName: r.ContainerName}
}

func fromProtoSnapshot(in *acceleratorv1.ResolvedGpuRequest, request *gpu.Request) (*gpu.Snapshot, error) {
	if in == nil || in.Execution == nil || in.Selection == nil {
		return nil, fmt.Errorf("accelerator returned an incomplete GPU execution")
	}
	execution := &gpu.Execution{SchedulerName: in.Execution.SchedulerName, QueueName: in.Execution.QueueName, RuntimeClassName: in.Execution.RuntimeClassName, NodeSelector: cloneMap(in.Execution.NodeSelector), PodAnnotations: cloneMap(in.Execution.PodAnnotations), Resources: cloneMap(in.Execution.Resources), ExecutionDigest: in.Execution.ExecutionDigest}
	display := &gpu.Display{ProfileID: in.Selection.ProfileId, ProfileVersion: in.Selection.ProfileVersion, ProfileName: in.Selection.ProfileName, ModelKey: in.Selection.ModelKey, Mode: int32(in.Selection.Mode)}
	quotas := make([]gpu.QuotaItem, 0, len(in.QuotaItems))
	for _, item := range in.QuotaItems {
		if item != nil {
			quotas = append(quotas, gpu.QuotaItem{QuotaCode: item.QuotaCode, Units: item.Units})
		}
	}
	snapshot := &gpu.Snapshot{Request: request, Execution: execution, Display: display, QuotaItems: quotas, ExecutionDigest: execution.ExecutionDigest}
	if err := gpu.ValidateSnapshot(snapshot, request); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
