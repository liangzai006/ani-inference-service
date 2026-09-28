package accelerator

import (
	"fmt"

	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/gpu"
)

func toProtoRequest(r *gpu.Request) *acceleratorv1.GpuRequest {
	if r == nil {
		return nil
	}
	return &acceleratorv1.GpuRequest{
		ClusterId:         r.ClusterID,
		PoolId:            r.PoolID,
		ProfileId:         r.ProfileID,
		ProfileVersion:    r.ProfileVersion,
		Replicas:          r.Replicas,
		DevicesPerReplica: r.DevicesPerReplica,
		ContainerName:     r.ContainerName,
	}
}

func fromProtoPlan(in *acceleratorv1.ResolvedGpuPlan) (*gpu.Plan, error) {
	if in == nil || in.Request == nil || in.Profile == nil || in.Profile.Spec == nil || in.Encoding == nil || in.Totals == nil || in.Runtime == nil {
		return nil, fmt.Errorf("accelerator returned an incomplete GPU plan")
	}
	return &gpu.Plan{
		SchemaVersion:    in.SchemaVersion,
		Request:          fromProtoRequest(in.Request),
		Profile:          fromProtoProfile(in.Profile),
		Encoding:         fromProtoEncoding(in.Encoding),
		Totals:           fromProtoTotals(in.Totals),
		Runtime:          fromProtoRuntime(in.Runtime),
		BaselineDigest:   in.BaselineDigest,
		ResolutionDigest: in.ResolutionDigest,
	}, nil
}

func fromProtoRequest(in *acceleratorv1.GpuRequest) *gpu.Request {
	if in == nil {
		return nil
	}
	return &gpu.Request{
		ClusterID:         in.ClusterId,
		PoolID:            in.PoolId,
		ProfileID:         in.ProfileId,
		ProfileVersion:    in.ProfileVersion,
		Replicas:          in.Replicas,
		DevicesPerReplica: in.DevicesPerReplica,
		ContainerName:     in.ContainerName,
	}
}

func fromProtoProfile(in *acceleratorv1.GpuProfile) *gpu.Profile {
	if in == nil {
		return nil
	}
	return &gpu.Profile{
		ProfileID:      in.ProfileId,
		ProfileVersion: in.ProfileVersion,
		DisplayName:    in.DisplayName,
		Spec:           fromProtoProfileSpec(in.Spec),
		BaselineID:     in.BaselineId,
		SpecDigest:     in.SpecDigest,
		Published:      in.Published,
	}
}

func fromProtoProfileSpec(in *acceleratorv1.ProfileSpec) *gpu.ProfileSpec {
	if in == nil {
		return nil
	}
	return &gpu.ProfileSpec{
		GroupID:              in.GroupId,
		Mode:                 int32(in.Mode),
		ModelKey:             in.ModelKey,
		SharedMemoryMiB:      in.SharedMemoryMib,
		CoreLimitPercent:     in.CoreLimitPercent,
		MaxDevicesPerReplica: in.MaxDevicesPerReplica,
		IsolationClass:       in.IsolationClass,
	}
}

func fromProtoEncoding(in *acceleratorv1.MemoryEncoding) *gpu.MemoryEncoding {
	if in == nil {
		return nil
	}
	return &gpu.MemoryEncoding{
		MemoryBlockMiB:        in.MemoryBlockMib,
		MemoryBlocksPerDevice: in.MemoryBlocksPerDevice,
		SharedMemoryMiB:       in.SharedMemoryMib,
		MemoryPercentage:      in.MemoryPercentage,
		Policy:                in.Policy,
	}
}

func fromProtoTotals(in *acceleratorv1.ResourceTotals) *gpu.ResourceTotals {
	if in == nil {
		return nil
	}
	return &gpu.ResourceTotals{
		LogicalDeviceCount:   in.LogicalDeviceCount,
		ExclusiveDeviceCount: in.ExclusiveDeviceCount,
		SharedMemoryMiB:      in.SharedMemoryMib,
	}
}

func fromProtoRuntime(in *acceleratorv1.RuntimeFragment) *gpu.RuntimeFragment {
	if in == nil {
		return nil
	}
	return &gpu.RuntimeFragment{
		SchedulerName:      in.SchedulerName,
		QueueName:          in.QueueName,
		NodeLabels:         fromProtoKeyValues(in.NodeLabels),
		PodAnnotations:     fromProtoKeyValues(in.PodAnnotations),
		LimitsPerContainer: fromProtoKeyValues(in.LimitsPerContainer),
		RuntimeClassName:   in.RuntimeClassName,
		RecipeVersion:      in.RecipeVersion,
	}
}

func fromProtoKeyValues(in []*acceleratorv1.KeyValue) []gpu.KeyValue {
	if in == nil {
		return nil
	}
	out := make([]gpu.KeyValue, 0, len(in))
	for _, item := range in {
		if item == nil {
			out = append(out, gpu.KeyValue{})
			continue
		}
		out = append(out, gpu.KeyValue{Key: item.Key, Value: item.Value})
	}
	return out
}
