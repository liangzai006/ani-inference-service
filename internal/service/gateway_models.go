package service

import (
	"context"
	"time"

	"github.com/liangzai006/ani-inference-service/internal/gatewayapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The gateway-only service never accepts caller-supplied tenant metadata.
type GatewayModelsServer struct {
	gatewayapi.UnimplementedModelDiscoveryServer
	Keys *InferenceAccessKeyServer
}

func (s *GatewayModelsServer) ListModels(ctx context.Context, req *gatewayapi.ListModelsRequest) (*gatewayapi.ListModelsResponse, error) {
	if s == nil || s.Keys == nil || s.Keys.service == nil {
		return nil, status.Error(codes.Unavailable, "inference model discovery unavailable")
	}
	decision, models, err := s.Keys.service.ListModels(ctx, req.GetApiKey())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "inference model discovery unavailable")
	}
	response := &gatewayapi.ListModelsResponse{Allowed: decision.Allowed, DenialCode: decision.DenialCode}
	if decision.RetryAfter > 0 {
		response.RetryAfterSeconds = int32((decision.RetryAfter + time.Second - 1) / time.Second)
	}
	if decision.Allowed {
		for _, model := range models {
			response.Models = append(response.Models, &gatewayapi.Model{Id: model.ID, Created: model.Created.Unix()})
		}
	}
	return response, nil
}
