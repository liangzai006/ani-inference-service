package model

import (
	"context"
	"slices"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateUseCase resolves authoritative Model metadata before Inference's own
// aggregate transaction. The signed URL is acquired at download time, never
// stored in the durable spec. This does not mark a runtime model ready.
type CreateUseCase struct {
	client *Client
	next   inference.CreateUseCase
}

func NewCreateUseCase(client *Client, next inference.CreateUseCase) *CreateUseCase {
	return &CreateUseCase{client: client, next: next}
}
func (u *CreateUseCase) Create(ctx context.Context, in inference.CreateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.next == nil {
		return nil, status.Error(codes.FailedPrecondition, "Inference create use case not configured")
	}
	v, err := u.client.GetReadyVersion(ctx, in.TenantID, in.ModelVersionID)
	if err != nil {
		return nil, err
	}
	// Runtime override policy is not defined yet: accept omitted or identical
	// defaults, and fail closed for a conflicting caller-supplied command.
	if (in.EngineRuntime != "" && in.EngineRuntime != v.EngineRuntime) || (len(in.CommandArgv) > 0 && !slices.Equal(in.CommandArgv, v.CommandArgv)) {
		return nil, status.Error(codes.InvalidArgument, "engine overrides must match Model defaults")
	}
	if (in.ArtifactRef != "" && in.ArtifactRef != v.ArtifactRef) || (in.ArtifactSHA256 != "" && in.ArtifactSHA256 != v.ArtifactSHA256) {
		return nil, status.Error(codes.InvalidArgument, "artifact does not match Model version")
	}
	in.ArtifactProvider, in.ArtifactRef, in.ArtifactSHA256 = "model", v.ArtifactRef, v.ArtifactSHA256
	in.EngineRuntime, in.CommandArgv = v.EngineRuntime, v.CommandArgv
	return u.next.Create(ctx, in)
}
