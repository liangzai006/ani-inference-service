package image

import (
	"context"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type runtimeBoundary struct {
	imagev1.ImageRuntimeServiceClient
	response *imagev1.ResolveImageForWorkloadResponse
	err      error
	calls    int
	metadata metadata.MD
}

func (r *runtimeBoundary) ResolveImageForWorkload(ctx context.Context, _ *imagev1.ResolveImageForWorkloadRequest, _ ...grpc.CallOption) (*imagev1.ResolveImageForWorkloadResponse, error) {
	r.calls++
	r.metadata, _ = metadata.FromOutgoingContext(ctx)
	return r.response, r.err
}

func TestResolveFencesScopePlatformDigestAndMetadata(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	const id = "22222222-2222-4222-8222-222222222222"
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runtime := &runtimeBoundary{response: &imagev1.ResolveImageForWorkloadResponse{TenantId: tenant, ImageId: id, Scope: imagev1.ImageScope_IMAGE_SCOPE_PLATFORM, ResolvedReference: "private.registry/platform/engine@" + digest, Digest: digest, SelectedPlatform: &imagev1.ImagePlatform{Os: "linux", Architecture: "amd64"}}}
	client := &Client{Runtime: runtime, Platform: &imagev1.ImagePlatform{Os: "linux", Architecture: "amd64"}, Namespace: "runtime"}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "must-not-forward", "x-ani-actor-id", "spoofed"))
	ref, selection, err := client.Resolve(ctx, tenant, id, "platform")
	if err != nil || ref != runtime.response.ResolvedReference || selection.PullSecretRef != biz.PullSecretName(tenant, "runtime", "private.registry") {
		t.Fatalf("resolution=%+v err=%v", selection, err)
	}
	if len(runtime.metadata.Get("authorization")) != 0 || len(runtime.metadata.Get("x-ani-actor-id")) != 0 || runtime.metadata.Get("x-ani-tenant-id")[0] != tenant {
		t.Fatal("unsafe metadata forwarded")
	}
	runtime.response.TenantId = id
	if _, _, err := client.Resolve(ctx, tenant, id, "platform"); status.Code(err) != codes.Internal {
		t.Fatalf("foreign tenant accepted: %v", err)
	}
	runtime.response.TenantId = tenant
	runtime.response.SelectedPlatform.Architecture = "arm64"
	if _, _, err := client.Resolve(ctx, tenant, id, "platform"); status.Code(err) != codes.Internal {
		t.Fatalf("platform substitution accepted: %v", err)
	}
}

type replayBoundary struct {
	handled bool
	calls   int
	checks  int
	after   int
}

func (r *replayBoundary) PrepareCreate(context.Context, biz.CreateInput) (*inferencev1.OperationResponse, bool, error) {
	r.checks++
	return &inferencev1.OperationResponse{}, r.handled || (r.after > 0 && r.checks > r.after), nil
}
func (r *replayBoundary) Create(context.Context, biz.CreateInput) (*inferencev1.OperationResponse, error) {
	r.calls++
	return &inferencev1.OperationResponse{}, nil
}

func TestCreateReplaysBeforeRegistryAndClassifiesOnlyStructuredDenial(t *testing.T) {
	runtime := &runtimeBoundary{err: status.Error(codes.Unavailable, "offline")}
	durable := &replayBoundary{handled: true}
	uc := &CreateUseCase{Client: &Client{Runtime: runtime}, Next: durable, Preflight: durable}
	if _, err := uc.Create(context.Background(), biz.CreateInput{ImageID: "image"}); err != nil || runtime.calls != 0 || durable.calls != 0 {
		t.Fatalf("replay reached remote: %v", err)
	}
	for _, err := range []error{status.Error(codes.NotFound, "IMAGE_NOT_FOUND"), status.Error(codes.DeadlineExceeded, "timeout"), status.Error(codes.PermissionDenied, "denied")} {
		if rejectionReason(err) != "" {
			t.Fatal("generic transport error became definitive rejection")
		}
	}
	st, _ := status.New(codes.NotFound, "IMAGE_NOT_FOUND").WithDetails(&errdetails.ErrorInfo{Domain: "image.ani.io", Reason: "IMAGE_NOT_FOUND"})
	if rejectionReason(st.Err()) != "IMAGE_NOT_FOUND" {
		t.Fatal("trusted business denial lost")
	}
}

func TestCreateUsesConcurrentAcceptanceBeforeReportingImageRejection(t *testing.T) {
	st, _ := status.New(codes.NotFound, "IMAGE_NOT_FOUND").WithDetails(&errdetails.ErrorInfo{Domain: "image.ani.io", Reason: "IMAGE_NOT_FOUND"})
	runtime := &runtimeBoundary{err: st.Err()}
	durable := &replayBoundary{after: 1}
	uc := &CreateUseCase{Client: &Client{Runtime: runtime, Platform: &imagev1.ImagePlatform{Os: "linux", Architecture: "amd64"}, Namespace: "runtime"}, Next: durable, Preflight: durable}
	out, err := uc.Create(context.Background(), biz.CreateInput{TenantID: "11111111-1111-4111-8111-111111111111", ImageID: "22222222-2222-4222-8222-222222222222", ImageScope: "platform"})
	if err != nil || out == nil || durable.checks != 2 || runtime.calls != 1 || durable.calls != 0 {
		t.Fatalf("concurrent receipt lost: out=%v err=%v", out, err)
	}
}
