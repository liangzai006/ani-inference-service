package model

import (
	"context"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UpdateUseCase refreshes the authoritative Model artifact snapshot when a
// caller selects another version. Model supplies artifact metadata only; it
// never fills or changes the request-owned engine type or startup argv.
type UpdateUseCase struct {
	client *Client
	next   inference.UpdateUseCase
}

func NewUpdateUseCase(client *Client, next inference.UpdateUseCase) *UpdateUseCase {
	return &UpdateUseCase{client: client, next: next}
}

func (u *UpdateUseCase) Update(ctx context.Context, in inference.UpdateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.next == nil {
		return nil, status.Error(codes.FailedPrecondition, "Inference update use case not configured")
	}
	if in.ModelVersionID == "" {
		if in.ArtifactProvider != "" || in.ArtifactRef != "" || in.ArtifactSHA256 != "" {
			return nil, status.Error(codes.InvalidArgument, "artifact changes require model_version_id")
		}
		return u.next.Update(ctx, in)
	}
	v, err := u.client.GetReadyVersion(ctx, in.TenantID, in.ModelVersionID)
	if err != nil {
		return nil, err
	}
	if (in.ArtifactRef != "" && in.ArtifactRef != v.ArtifactRef) || (in.ArtifactSHA256 != "" && in.ArtifactSHA256 != v.ArtifactSHA256) {
		return nil, status.Error(codes.InvalidArgument, "artifact does not match Model version")
	}
	in.ArtifactProvider, in.ArtifactRef, in.ArtifactSHA256 = "model", v.ArtifactRef, v.ArtifactSHA256
	return u.next.Update(ctx, in)
}
