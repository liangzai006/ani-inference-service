package model

import (
	"context"
	"strings"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateUseCase resolves authoritative Model artifact metadata before
// Inference's own aggregate transaction. Model supplies artifact metadata only;
// the request must supply the engine type and complete startup argv. The signed
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
	if strings.TrimSpace(in.EngineRuntime) == "" || len(in.CommandArgv) == 0 {
		return nil, status.Error(codes.InvalidArgument, "engine type and complete startup command are required")
	}
	v, err := u.client.GetReadyVersion(ctx, in.TenantID, in.ModelVersionID)
	if err != nil {
		return nil, err
	}
	if (in.ArtifactRef != "" && in.ArtifactRef != v.ArtifactRef) || (in.ArtifactSHA256 != "" && in.ArtifactSHA256 != v.ArtifactSHA256) {
		return nil, status.Error(codes.InvalidArgument, "artifact does not match Model version")
	}
	in.ArtifactProvider, in.ArtifactRef, in.ArtifactSHA256 = "model", v.ArtifactRef, v.ArtifactSHA256
	return u.next.Create(ctx, in)
}
