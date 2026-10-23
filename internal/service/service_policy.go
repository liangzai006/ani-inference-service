package service

import (
	"context"
	"errors"
	"strings"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	policybiz "github.com/liangzai006/ani-inference-service/internal/biz/servicepolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *InferenceServer) SetServicePolicy(p *policybiz.Service) { s.policy = p }
func (s *InferenceServer) GetServicePolicy(ctx context.Context, in *inferencev1.GetServicePolicyRequest) (*inferencev1.GetServicePolicyResponse, error) {
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || !policybiz.ValidID(in.GetResourceId()) {
		return nil, status.Error(codes.InvalidArgument, "resource_id must be a canonical UUID")
	}
	if s.policy == nil {
		return nil, status.Error(codes.FailedPrecondition, "service policy is not configured")
	}
	p, err := s.policy.Get(ctx, tenant, in.ResourceId)
	if err != nil {
		return nil, servicePolicyError(err)
	}
	return &inferencev1.GetServicePolicyResponse{Policy: servicePolicyProto(p)}, nil
}
func (s *InferenceServer) UpdateServicePolicy(ctx context.Context, in *inferencev1.UpdateServicePolicyRequest) (*inferencev1.UpdateServicePolicyResponse, error) {
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || in.Policy == nil || in.Policy.UpdatedAt != nil || !policybiz.ValidID(in.GetResourceId()) || (in.Policy.ResourceId != "" && in.Policy.ResourceId != in.ResourceId) {
		return nil, status.Error(codes.InvalidArgument, "resource_id and policy are required and must match")
	}
	if s.policy == nil {
		return nil, status.Error(codes.FailedPrecondition, "service policy is not configured")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	requests := md.Get("x-ani-request-id")
	if len(requests) != 1 || strings.TrimSpace(requests[0]) == "" {
		return nil, status.Error(codes.InvalidArgument, "trusted request ID is required")
	}
	p := policybiz.Policy{ResourceID: in.ResourceId, AllowedTenants: in.Policy.AllowedTenants, RateLimits: map[string]policybiz.RateLimit{}}
	for id, limit := range in.Policy.RateLimits {
		if limit == nil {
			return nil, status.Error(codes.InvalidArgument, "rate limit cannot be null")
		}
		p.RateLimits[id] = policybiz.RateLimit{RequestsPerMinute: limit.RequestsPerMinute, RequestsPerHour: limit.RequestsPerHour}
	}
	out, err := s.policy.Update(ctx, tenant, Actor(ctx), requests[0], p)
	if err != nil {
		return nil, servicePolicyError(err)
	}
	return &inferencev1.UpdateServicePolicyResponse{Policy: servicePolicyProto(out)}, nil
}
func servicePolicyProto(p policybiz.Policy) *inferencev1.ServicePolicy {
	out := &inferencev1.ServicePolicy{ResourceId: p.ResourceID, AllowedTenants: p.AllowedTenants, RateLimits: map[string]*inferencev1.RateLimitConfig{}, UpdatedAt: timestamppb.New(p.UpdatedAt)}
	for id, v := range p.RateLimits {
		out.RateLimits[id] = &inferencev1.RateLimitConfig{RequestsPerMinute: v.RequestsPerMinute, RequestsPerHour: v.RequestsPerHour}
	}
	return out
}
func servicePolicyError(err error) error {
	switch {
	case errors.Is(err, policybiz.ErrNotFound):
		return status.Error(codes.NotFound, "inference service not found")
	case errors.Is(err, policybiz.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, "invalid service policy")
	default:
		return status.Error(codes.Unavailable, "service policy unavailable")
	}
}
