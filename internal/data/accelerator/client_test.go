package accelerator

import (
	"context"
	"net"
	"strings"
	"testing"

	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/gpu"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func validGPURequest() *gpu.Request {
	return &gpu.Request{
		ClusterID:         "10000000-0000-4000-8000-000000000001",
		PoolID:            "10000000-0000-4000-8000-000000000002",
		ProfileID:         "10000000-0000-4000-8000-000000000003",
		ProfileVersion:    1,
		Replicas:          1,
		DevicesPerReplica: 1,
		ContainerName:     "kserve-container",
	}
}

func TestClientDoesNotContactResolverWithoutGPU(t *testing.T) {
	var c *Client
	plan, err := c.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant", RequestID: "request"})
	if err != nil || plan != nil {
		t.Fatalf("nil GPU request: plan=%v err=%v", plan, err)
	}
}

func TestClientRequiresResolverForGPU(t *testing.T) {
	request := validGPURequest()
	var c *Client
	_, err := c.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant", RequestID: "request", Actor: "workload:caller", Request: request})
	if err != ErrResolverNotConfigured {
		t.Fatalf("error=%v, want ErrResolverNotConfigured", err)
	}
}

func TestClientRejectsDifferentConfiguredCluster(t *testing.T) {
	client := NewClientForCluster(nil, "10000000-0000-4000-8000-000000000099")
	_, err := client.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant", RequestID: "request", Actor: "workload:caller", Request: validGPURequest()})
	if err == nil || !strings.Contains(err.Error(), "does not match configured cluster") {
		t.Fatalf("error=%v, want configured-cluster rejection", err)
	}
}

type catalogServer struct {
	acceleratorv1.UnimplementedAcceleratorCatalogServiceServer
	plan *acceleratorv1.ResolvedGpuPlan
	got  *acceleratorv1.ResolveGpuRequestRequest
	md   metadata.MD
}

func (s *catalogServer) ResolveGpuRequest(ctx context.Context, req *acceleratorv1.ResolveGpuRequestRequest) (*acceleratorv1.ResolvedGpuPlan, error) {
	s.got = req
	s.md, _ = metadata.FromIncomingContext(ctx)
	return s.plan, nil
}

func TestClientMapsRequestPlanAndIdentity(t *testing.T) {
	request := validGPURequest()
	plan := &gpu.Plan{
		SchemaVersion: 1,
		Request:       request,
		Profile: &gpu.Profile{
			ProfileID: request.ProfileID, ProfileVersion: 1,
			Spec: &gpu.ProfileSpec{GroupID: "10000000-0000-4000-8000-000000000004"},
		},
		Encoding: &gpu.MemoryEncoding{MemoryPercentage: 100, Policy: "EXACT"},
		Totals:   &gpu.ResourceTotals{ExclusiveDeviceCount: 1},
		Runtime:  &gpu.RuntimeFragment{SchedulerName: "volcano", NodeLabels: []gpu.KeyValue{{Key: "z", Value: "1"}, {Key: "a", Value: "2"}}},
	}
	digest, err := gpu.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.ResolutionDigest = digest
	server := &catalogServer{plan: toProtoPlanForTest(plan)}
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	acceleratorv1.RegisterAcceleratorCatalogServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() { grpcServer.Stop(); _ = listener.Close() })
	conn, err := grpc.DialContext(context.Background(), "passthrough:///accelerator", grpc.WithInsecure(), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := NewClientForCluster(acceleratorv1.NewAcceleratorCatalogServiceClient(conn), request.ClusterID)
	got, err := client.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant-1", RequestID: "request-1", Actor: "workload:caller", Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolutionDigest != digest || got.Request.ClusterID != request.ClusterID {
		t.Fatalf("mapped plan=%+v", got)
	}
	if server.got == nil || server.got.Context.GetTenantId() != "tenant-1" || server.got.Context.GetRequestId() != "request-1" || server.got.Context.GetActor().GetType() != "workload" || server.got.Context.GetActor().GetId() != "caller" {
		t.Fatalf("request context=%v", server.got.GetContext())
	}
	if gotMD := server.md.Get("x-ani-action"); len(gotMD) != 1 || gotMD[0] != "ResolveGpuRequest" {
		t.Fatalf("metadata=%v", server.md)
	}
}

func toProtoPlanForTest(plan *gpu.Plan) *acceleratorv1.ResolvedGpuPlan {
	profileSpec := plan.Profile.Spec
	labels := func(in []gpu.KeyValue) []*acceleratorv1.KeyValue {
		out := make([]*acceleratorv1.KeyValue, 0, len(in))
		for _, item := range in {
			item := item
			out = append(out, &acceleratorv1.KeyValue{Key: item.Key, Value: item.Value})
		}
		return out
	}
	return &acceleratorv1.ResolvedGpuPlan{
		SchemaVersion:  plan.SchemaVersion,
		Request:        toProtoRequest(plan.Request),
		Profile:        &acceleratorv1.GpuProfile{ProfileId: plan.Profile.ProfileID, ProfileVersion: plan.Profile.ProfileVersion, Spec: &acceleratorv1.ProfileSpec{GroupId: profileSpec.GroupID, Mode: acceleratorv1.SupplyMode(profileSpec.Mode), ModelKey: profileSpec.ModelKey, SharedMemoryMib: profileSpec.SharedMemoryMiB, CoreLimitPercent: profileSpec.CoreLimitPercent, MaxDevicesPerReplica: profileSpec.MaxDevicesPerReplica, IsolationClass: profileSpec.IsolationClass}, BaselineId: plan.Profile.BaselineID, SpecDigest: plan.Profile.SpecDigest, Published: plan.Profile.Published},
		Encoding:       &acceleratorv1.MemoryEncoding{MemoryBlockMib: plan.Encoding.MemoryBlockMiB, MemoryBlocksPerDevice: plan.Encoding.MemoryBlocksPerDevice, SharedMemoryMib: plan.Encoding.SharedMemoryMiB, MemoryPercentage: plan.Encoding.MemoryPercentage, Policy: plan.Encoding.Policy},
		Totals:         &acceleratorv1.ResourceTotals{LogicalDeviceCount: plan.Totals.LogicalDeviceCount, ExclusiveDeviceCount: plan.Totals.ExclusiveDeviceCount, SharedMemoryMib: plan.Totals.SharedMemoryMiB},
		Runtime:        &acceleratorv1.RuntimeFragment{SchedulerName: plan.Runtime.SchedulerName, QueueName: plan.Runtime.QueueName, NodeLabels: labels(plan.Runtime.NodeLabels), PodAnnotations: labels(plan.Runtime.PodAnnotations), LimitsPerContainer: labels(plan.Runtime.LimitsPerContainer), RuntimeClassName: plan.Runtime.RuntimeClassName, RecipeVersion: plan.Runtime.RecipeVersion},
		BaselineDigest: plan.BaselineDigest, ResolutionDigest: plan.ResolutionDigest,
	}
}
