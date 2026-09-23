package model

import (
	"context"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateUseCase resolves authoritative Model artifact metadata before
// Inference's own aggregate transaction. Model runtime settings are defaults:
// an explicitly supplied engine type and argv remain caller-owned. The signed
// URL is acquired at download time, never stored in the durable spec. This does
// not mark a runtime model ready.
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
	if (in.ArtifactRef != "" && in.ArtifactRef != v.ArtifactRef) || (in.ArtifactSHA256 != "" && in.ArtifactSHA256 != v.ArtifactSHA256) {
		return nil, status.Error(codes.InvalidArgument, "artifact does not match Model version")
	}
	in.ArtifactProvider, in.ArtifactRef, in.ArtifactSHA256 = "model", v.ArtifactRef, v.ArtifactSHA256
	if in.EngineRuntime == "" {
		in.EngineRuntime = v.EngineRuntime
	}
	if len(in.CommandArgv) == 0 {
		in.CommandArgv = v.CommandArgv
	}
	return u.next.Create(ctx, in)
}
