package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagedata "github.com/liangzai006/ani-inference-service/internal/data/image"
	"github.com/liangzai006/ani-inference-service/internal/service"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Only the external Resource RPC is substituted; command verification,
// acceptance, immutable spec, replay, readback and closing fences are real.
type catalogBoundary struct {
	imagev1.ImageRuntimeServiceClient
	calls int
	err   error
}

func (r *catalogBoundary) ResolveImageForWorkload(_ context.Context, in *imagev1.ResolveImageForWorkloadRequest, _ ...grpc.CallOption) (*imagev1.ResolveImageForWorkloadResponse, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return &imagev1.ResolveImageForWorkloadResponse{TenantId: in.TenantId, ImageId: in.ImageId, Scope: in.Scope, ResolvedReference: "private.registry/engine@" + digest, Digest: digest, RegistryAuthority: "private.registry", SelectedPlatform: in.TargetPlatform}, nil
}

// catalogPullSecretRef is the shared tenant scope name embedded in the frozen
// selection. cluster-a mirrors the runtime cluster ID configured for the client.
func catalogPullSecretRef(t *testing.T, tenant string) string {
	t.Helper()
	name, err := biz.PullSecretName(tenant, "cluster-a", "runtime", "private.registry")
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func TestCatalogImagePostgresAcceptanceReplayAndDeleteFence(t *testing.T) {
	for _, gpuResource := range []bool{false, true} {
		name := "cpu"
		if gpuResource {
			name = "gpu"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, tenant, resource := lifecycleDatabase(t)
			ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
			req := managedCreateFixture(t, tenant, resource)
			if !gpuResource {
				req.Resource.Gpu = nil
				req.ClusterId = ""
				req.GpuExecution = nil
				req.GpuDisplay = nil
			}
			req.ContractVersion = 2
			req.Engine.Image = ""
			req.Engine.ImageId = "img_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			req.Engine.ImageScope = "platform"
			refreshManagedFixtureDigests(t, req)
			boundary := &catalogBoundary{}
			newServer := func() *service.InferenceServer {
				durable := NewCreateUseCase(NewRepository(pool))
				uc := &imagedata.CreateUseCase{Client: &imagedata.Client{Runtime: boundary, Platform: &imagev1.ImagePlatform{Os: "linux", Architecture: "amd64"}, Namespace: "runtime", ClusterID: "cluster-a", RequireCatalog: true}, Next: durable, Preflight: durable}
				s := service.NewInferenceServerWithAll(uc, NewReadUseCase(pool), NewCommandUseCase(NewRepository(pool)), NewUpdateUseCase(NewRepository(pool)))
				s.SetManagedGPUClusterID(req.ClusterId)
				return s
			}
			out, err := newServer().CreateInferenceService(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			spec, err := New(pool).GetSpec(ctx, GetSpecParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(resource), Generation: 1})
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Image *biz.ImageSelection `json:"image_selection"`
			}
			if json.Unmarshal(spec.SpecJson, &saved) != nil || saved.Image == nil || saved.Image.ImageID != req.Engine.ImageId || saved.Image.PullSecretRef != catalogPullSecretRef(t, tenant.String()) || spec.ImageRef != out.GetResource().GetEngine().GetImage() {
				t.Fatal("accepted image not persisted")
			}
			boundary.err = status.Error(codes.Unavailable, "directory offline")
			replay, err := newServer().CreateInferenceService(ctx, req)
			if err != nil || !proto.Equal(out, replay) || boundary.calls != 1 {
				t.Fatalf("restart replay changed or called registry: %v", err)
			}
			changed := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
			changed.Engine.ImageId = "img_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			refreshManagedFixtureDigests(t, changed)
			if _, err := newServer().CreateInferenceService(ctx, changed); status.Code(err) != codes.AlreadyExists || boundary.calls != 1 {
				t.Fatalf("changed selection did not conflict: %v", err)
			}
			// A separate never-accepted resource exercises P1 and DELETE-first.
			late := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
			late.Context.ResourceId = uuid.NewString()
			late.Context.OperationId = uuid.NewString()
			refreshManagedFixtureDigests(t, late)
			denial, _ := status.New(codes.NotFound, "IMAGE_NOT_FOUND").WithDetails(&errdetails.ErrorInfo{Domain: "image.ani.io", Reason: "IMAGE_NOT_FOUND"})
			boundary.err = denial.Err()
			if _, err := newServer().CreateInferenceService(ctx, late); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("definitive denial=%v", err)
			}
			del := managedDeleteFixture(t, late)
			if _, err := newServer().DeleteInferenceService(ctx, del); err != nil {
				t.Fatal(err)
			}
			calls := boundary.calls
			boundary.err = nil
			blocked, err := newServer().CreateInferenceService(ctx, late)
			if err != nil || !blocked.GetDurableOwnerAck().GetAccepted() || boundary.calls != calls {
				t.Fatalf("late CREATE fence receipt: %v", err)
			}
			op, err := NewReadUseCase(pool).GetOperation(ctx, tenant.String(), late.Context.OperationId)
			if err != nil || op.GetPhase() != "failed" || op.GetStep() != "cancelled_by_delete" || op.GetTargetGeneration() != 0 {
				t.Fatalf("late CREATE resurrected: %v %v", op, err)
			}
		})
	}
}
