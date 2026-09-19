package service

import (
	"context"

	"github.com/google/uuid"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ModelReferenceServer struct {
	inferencev1.UnimplementedModelReferenceServiceServer
	reader inferencebiz.ModelReferenceReader
}

func NewModelReferenceServer(reader inferencebiz.ModelReferenceReader) *ModelReferenceServer {
	return &ModelReferenceServer{reader: reader}
}
func (s *ModelReferenceServer) CheckModelVersionReferences(ctx context.Context, req *inferencev1.CheckModelVersionReferencesRequest) (*inferencev1.CheckModelVersionReferencesResponse, error) {
	tenant, ok := TenantID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "trusted tenant identity required")
	}
	if len(req.GetModelVersionIds()) < 1 || len(req.GetModelVersionIds()) > 256 {
		return nil, status.Error(codes.InvalidArgument, "expected 1 to 256 model version IDs")
	}
	ids := make([]string, len(req.GetModelVersionIds()))
	for i, v := range req.GetModelVersionIds() {
		id, err := uuid.Parse(v)
		if err != nil || id == uuid.Nil {
			return nil, status.Error(codes.InvalidArgument, "invalid model version ID")
		}
		ids[i] = id.String()
	}
	if s.reader == nil {
		return nil, status.Error(codes.Unavailable, "reference store not configured")
	}
	active, err := s.reader.HasActiveModelVersionReferences(ctx, tenant, ids)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "reference lookup unavailable")
	}
	return &inferencev1.CheckModelVersionReferencesResponse{HasActiveReferences: active}, nil
}
