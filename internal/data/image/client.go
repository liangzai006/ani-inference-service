package image

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Client struct {
	Runtime        imagev1.ImageRuntimeServiceClient
	Platform       *imagev1.ImagePlatform
	Namespace      string
	ClusterID      string
	RequireCatalog bool
}

func runtimeContext(ctx context.Context, tenant string) (context.Context, context.CancelFunc) {
	// Do not forward incoming user/actor/delegation headers to the runtime RPC.
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-request-id", uuid.NewString()))
	return context.WithTimeout(ctx, 10*time.Second)
}

var fixedDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (c *Client) Resolve(ctx context.Context, tenant, id, scope string) (string, *biz.ImageSelection, error) {
	if c == nil || c.Runtime == nil || c.Platform == nil || c.Namespace == "" || c.ClusterID == "" {
		return "", nil, status.Error(codes.FailedPrecondition, "image runtime is not configured")
	}
	wireScope := imagev1.ImageScope_IMAGE_SCOPE_TENANT
	if scope == "platform" {
		wireScope = imagev1.ImageScope_IMAGE_SCOPE_PLATFORM
	} else if scope != "tenant" {
		return "", nil, status.Error(codes.InvalidArgument, "invalid image scope")
	}
	callCtx, cancel := runtimeContext(ctx, tenant)
	defer cancel()
	resolved, err := c.Runtime.ResolveImageForWorkload(callCtx, &imagev1.ResolveImageForWorkloadRequest{TenantId: tenant, ImageId: id, Scope: wireScope, TargetPlatform: c.Platform})
	if err != nil {
		return "", nil, err
	}
	p := resolved.GetSelectedPlatform()
	ref := resolved.GetResolvedReference()
	// The registry authority is Resource's authoritative value. It is never
	// re-derived from the reference: case, port and mirror aliases must not be
	// able to make the two services disagree on the Secret hash input.
	registry := resolved.GetRegistryAuthority()
	if resolved.GetTenantId() != tenant || resolved.GetImageId() != id || resolved.GetScope() != wireScope || !fixedDigest.MatchString(resolved.GetDigest()) || !strings.HasSuffix(ref, "@"+resolved.GetDigest()) ||
		registry == "" || !strings.HasPrefix(ref, registry+"/") || p.GetOs() != c.Platform.GetOs() || p.GetArchitecture() != c.Platform.GetArchitecture() || p.GetVariant() != c.Platform.GetVariant() {
		return "", nil, status.Error(codes.Internal, "invalid image runtime resolution")
	}
	name, err := biz.PullSecretName(tenant, c.ClusterID, c.Namespace, registry)
	if err != nil {
		return "", nil, status.Error(codes.Internal, "invalid pull Secret scope")
	}
	return ref, &biz.ImageSelection{ImageID: id, Scope: scope, OS: p.GetOs(), Architecture: p.GetArchitecture(), Variant: p.GetVariant(), RegistryAuthority: registry, PullSecretRef: name}, nil
}

// EnsurePullSecret asks Resource to write the shared Secret and returns only
// the non-sensitive reference. Inference no longer reads plaintext material or
// writes the Secret itself.
func (c *Client) EnsurePullSecret(ctx context.Context, tenant, namespace, namespaceUID, registry string) (*imagev1.EnsureTenantPullSecretResponse, error) {
	if c == nil || c.Runtime == nil || c.ClusterID == "" {
		return nil, status.Error(codes.FailedPrecondition, "image runtime is not configured")
	}
	callCtx, cancel := runtimeContext(ctx, tenant)
	defer cancel()
	return c.Runtime.EnsureTenantPullSecret(callCtx, &imagev1.EnsureTenantPullSecretRequest{TenantId: tenant, ClusterId: c.ClusterID, Namespace: namespace, NamespaceUid: namespaceUID, RegistryAuthority: registry})
}

// Only structured business denials from the authenticated Resource runtime
// qualify. Generic NotFound, permission, transport or dependency errors do not.
func rejectionReason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "image.ani.io" {
			continue
		}
		switch info.GetReason() {
		case "IMAGE_NOT_FOUND", "SPACE_NOT_FOUND", "SPACE_NOT_READY", "IMAGE_PROJECT_DENIED", "UNSUPPORTED_ARTIFACT", "PLATFORM_MISMATCH", "INVALID_REFERENCE", "SPACE_OWNERSHIP_UNCONFIRMED":
			return info.GetReason()
		}
	}
	return ""
}

func admissionError(err error, in biz.CreateInput) error {
	reason := rejectionReason(err)
	if reason == "" || in.Managed == nil {
		return err
	}
	command := in.Managed.Context
	st, detailErr := status.New(codes.FailedPrecondition, "IMAGE_ADMISSION_REJECTED").WithDetails(&errdetails.ErrorInfo{Domain: "ani-inference", Reason: "IMAGE_ADMISSION_REJECTED", Metadata: map[string]string{"stage": "pre_accept", "operation_id": command.OriginalCreateOperationID, "resource_id": command.ResourceID, "tenant_id": in.TenantID, "request_digest": in.Managed.CreateCommandDigest, "image_reason": reason}})
	if detailErr != nil {
		return status.Error(codes.Internal, "image admission detail unavailable")
	}
	return st.Err()
}

type CreateUseCase struct {
	Client    *Client
	Next      biz.CreateUseCase
	Preflight biz.CreatePreflight
}

func (u *CreateUseCase) Create(ctx context.Context, in biz.CreateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.Next == nil || u.Preflight == nil {
		return nil, errors.New("image create dependencies required")
	}
	if out, handled, err := u.Preflight.PrepareCreate(ctx, in); handled || err != nil {
		return out, err
	}
	if in.ImageID == "" {
		if u.Client != nil && u.Client.RequireCatalog {
			return nil, status.Error(codes.FailedPrecondition, "new inference requires catalog image selection")
		}
		return u.Next.Create(ctx, in)
	}
	ref, selection, err := u.Client.Resolve(ctx, in.TenantID, in.ImageID, in.ImageScope)
	if err != nil {
		// A concurrent delivery may have committed while Resource was in flight.
		// Prefer that durable receipt over a later catalog rejection.
		if out, handled, replayErr := u.Preflight.PrepareCreate(ctx, in); handled || replayErr != nil {
			return out, replayErr
		}
		return nil, admissionError(err, in)
	}
	in.ImageRef, in.ImageSelection = ref, selection
	return u.Next.Create(ctx, in)
}

type UpdateUseCase struct {
	Client    *Client
	Next      biz.UpdateUseCase
	Preflight biz.UpdatePreflight
}

func (u *UpdateUseCase) Update(ctx context.Context, in biz.UpdateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.Next == nil || u.Preflight == nil {
		return nil, errors.New("image update dependencies required")
	}
	if out, handled, err := u.Preflight.PrepareUpdate(ctx, in); handled || err != nil {
		return out, err
	}
	engineChanged := len(in.UpdateMask) == 0
	for _, path := range in.UpdateMask {
		if path == "engine" {
			engineChanged = true
		}
	}
	if !engineChanged {
		return u.Next.Update(ctx, in)
	}
	if in.ImageID == "" {
		if u.Client != nil && u.Client.RequireCatalog {
			return nil, status.Error(codes.FailedPrecondition, "image update requires catalog selection")
		}
		return u.Next.Update(ctx, in)
	}
	ref, selection, err := u.Client.Resolve(ctx, in.TenantID, in.ImageID, in.ImageScope)
	if err != nil {
		if out, handled, replayErr := u.Preflight.PrepareUpdate(ctx, in); handled || replayErr != nil {
			return out, replayErr
		}
		return nil, err
	}
	in.ImageRef, in.ImageSelection = ref, selection
	return u.Next.Update(ctx, in)
}
