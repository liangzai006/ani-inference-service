package accelerator

import (
	"context"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"strings"
	"testing"
)

func validGPURequest() *gpu.Request {
	return &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
}
func TestClientDoesNotContactResolverWithoutGPU(t *testing.T) {
	var c *Client
	got, err := c.Resolve(context.Background(), gpu.ResolveInput{})
	if err != nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestClientRequiresResolverForGPU(t *testing.T) {
	request := validGPURequest()
	var c *Client
	got, err := c.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant", RequestID: "request", Request: request})
	if got != nil || err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("nil resolver accepted: snapshot=%v err=%v", got, err)
	}
}
func TestClientRejectsDifferentConfiguredCluster(t *testing.T) {
	c := NewClientForCluster(nil, "10000000-0000-4000-8000-000000000099")
	_, err := c.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant", RequestID: "request", Actor: "workload:caller", Request: validGPURequest()})
	if err == nil || !strings.Contains(err.Error(), "does not match configured cluster") {
		t.Fatal(err)
	}
}

type catalogServer struct {
	acceleratorv1.UnimplementedAcceleratorCatalogServiceServer
	reply *acceleratorv1.ResolvedGpuRequest
	got   *acceleratorv1.ResolveGpuRequestRequest
	md    metadata.MD
}

func (s *catalogServer) ResolveGpuRequest(ctx context.Context, req *acceleratorv1.ResolveGpuRequestRequest) (*acceleratorv1.ResolvedGpuRequest, error) {
	s.got = req
	s.md, _ = metadata.FromIncomingContext(ctx)
	return s.reply, nil
}
func TestClientMapsExecutionAndIdentity(t *testing.T) {
	r := validGPURequest()
	execution := &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "queue", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "group", "accelerator.ani.io/model-key": "model", "accelerator.ani.io/baseline-id": "baseline"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "1", "volcano.sh/vgpu-cores": "25"}}
	execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(execution)
	reply := &acceleratorv1.ResolvedGpuRequest{Execution: execution, Selection: &acceleratorv1.GpuDisplay{ProfileId: r.ProfileID, ProfileVersion: 1, ProfileName: "shared", ModelKey: "model", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}, QuotaItems: []*acceleratorv1.GpuQuotaItem{{QuotaCode: "gpu.shared", Units: 1}}}
	srv := &catalogServer{reply: reply}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	acceleratorv1.RegisterAcceleratorCatalogServiceServer(gs, srv)
	go gs.Serve(lis)
	defer gs.Stop()
	conn, err := grpc.DialContext(context.Background(), "passthrough:///acc", grpc.WithInsecure(), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := NewClientForCluster(acceleratorv1.NewAcceleratorCatalogServiceClient(conn), r.ClusterID)
	got, err := c.Resolve(context.Background(), gpu.ResolveInput{TenantID: "tenant-1", RequestID: "request-1", Actor: "workload:caller", Request: r})
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution.QueueName != "queue" || got.Display.ProfileID != r.ProfileID || srv.got.GetContext().GetTenantId() != "tenant-1" {
		t.Fatalf("got=%+v req=%v", got, srv.got)
	}
	if srv.md.Get("x-ani-action")[0] != "ResolveGpuRequest" {
		t.Fatal(srv.md)
	}
}
