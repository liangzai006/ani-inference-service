package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policy "github.com/liangzai006/ani-inference-service/internal/biz/accesspolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *InferenceAccessKeyServer) policyRepository(ctx context.Context) (policy.Repository, string, error) {
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, "", err
	}
	if s == nil || s.service == nil {
		return nil, "", status.Error(codes.FailedPrecondition, "access policy is not configured")
	}
	repo, ok := s.service.Repository.(policy.Repository)
	if !ok {
		return nil, "", status.Error(codes.FailedPrecondition, "access policy repository is not configured")
	}
	return repo, tenant, nil
}
func validPolicyUUID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func policyRequestActor(ctx context.Context, request string) (string, error) {
	actor := strings.TrimSpace(Actor(ctx))
	if actor == "" || strings.TrimSpace(request) == "" {
		return "", status.Error(codes.InvalidArgument, "actor and request_id are required")
	}
	return actor, nil
}
func policyFromProto(p *inferencev1.InferenceAccessPolicy, tenant string, updating bool) (policy.Policy, error) {
	if p == nil || (updating && !validPolicyUUID(p.GetId())) || (!updating && (p.GetId() != "" || p.GetVersion() != 0)) {
		return policy.Policy{}, status.Error(codes.InvalidArgument, "invalid policy identity or version")
	}
	out, err := (policy.Policy{ID: p.GetId(), TenantID: tenant, Name: p.GetName(), AllowedModelNames: p.GetAllowedModelNames(), RequestsPerSecond: p.GetRequestsPerSecond(), RequestsPerMinute: p.GetRequestsPerMinute(), RequestsPerHour: p.GetRequestsPerHour()}).Normalize()
	if err != nil {
		return out, status.Error(codes.InvalidArgument, "invalid policy")
	}
	return out, nil
}
func policyProto(p policy.Policy) *inferencev1.InferenceAccessPolicy {
	return &inferencev1.InferenceAccessPolicy{Id: p.ID, Name: p.Name, AllowedModelNames: p.AllowedModelNames, RequestsPerSecond: p.RequestsPerSecond, RequestsPerMinute: p.RequestsPerMinute, RequestsPerHour: p.RequestsPerHour, Version: p.Version}
}
func policyStatus(err error) error {
	switch {
	case errors.Is(err, policy.ErrNotFound), errors.Is(err, accesskey.ErrNotFound):
		return status.Error(codes.NotFound, "policy or key not found")
	case errors.Is(err, policy.ErrInUse):
		return status.Error(codes.FailedPrecondition, "policy is bound to an access key")
	case errors.Is(err, policy.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, "invalid policy")
	default:
		return status.Error(codes.Unavailable, "policy repository unavailable")
	}
}
func (s *InferenceAccessKeyServer) CreateInferenceAccessPolicy(ctx context.Context, req *inferencev1.CreateInferenceAccessPolicyRequest) (*inferencev1.InferenceAccessPolicy, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	actor, err := policyRequestActor(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	p, err := policyFromProto(req.GetPolicy(), tenant, false)
	if err != nil {
		return nil, err
	}
	p, err = repo.CreateAccessPolicy(ctx, p, actor, req.GetRequestId())
	if err != nil {
		return nil, policyStatus(err)
	}
	return policyProto(p), nil
}
func (s *InferenceAccessKeyServer) GetInferenceAccessPolicy(ctx context.Context, req *inferencev1.GetInferenceAccessPolicyRequest) (*inferencev1.InferenceAccessPolicy, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || !validPolicyUUID(req.GetId()) {
		return nil, status.Error(codes.InvalidArgument, "valid id required")
	}
	p, err := repo.GetAccessPolicy(ctx, tenant, req.GetId())
	if err != nil {
		return nil, policyStatus(err)
	}
	return policyProto(p), nil
}
func (s *InferenceAccessKeyServer) ListInferenceAccessPolicies(ctx context.Context, req *inferencev1.ListInferenceAccessPoliciesRequest) (*inferencev1.ListInferenceAccessPoliciesResponse, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	page, size := req.GetPage(), req.GetPageSize()
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || page > 100 || size < 1 || size > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid page")
	}
	items, total, err := repo.ListAccessPolicies(ctx, tenant, int(size), int((page-1)*size))
	if err != nil {
		return nil, policyStatus(err)
	}
	out := &inferencev1.ListInferenceAccessPoliciesResponse{Total: total}
	for _, p := range items {
		out.Items = append(out.Items, policyProto(p))
	}
	return out, nil
}
func (s *InferenceAccessKeyServer) UpdateInferenceAccessPolicy(ctx context.Context, req *inferencev1.UpdateInferenceAccessPolicyRequest) (*inferencev1.InferenceAccessPolicy, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request required")
	}
	actor, err := policyRequestActor(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	p, err := policyFromProto(req.GetPolicy(), tenant, true)
	if err != nil {
		return nil, err
	}
	p, err = repo.UpdateAccessPolicy(ctx, p, actor, req.GetRequestId())
	if err != nil {
		return nil, policyStatus(err)
	}
	return policyProto(p), nil
}
func (s *InferenceAccessKeyServer) DeleteInferenceAccessPolicy(ctx context.Context, req *inferencev1.DeleteInferenceAccessPolicyRequest) (*inferencev1.DeleteInferenceAccessPolicyResponse, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || !validPolicyUUID(req.GetId()) {
		return nil, status.Error(codes.InvalidArgument, "valid id required")
	}
	actor, err := policyRequestActor(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	if err = repo.DeleteAccessPolicy(ctx, tenant, req.GetId(), actor, req.GetRequestId()); err != nil {
		return nil, policyStatus(err)
	}
	return &inferencev1.DeleteInferenceAccessPolicyResponse{}, nil
}
func (s *InferenceAccessKeyServer) BindInferenceAccessKeyPolicy(ctx context.Context, req *inferencev1.BindInferenceAccessKeyPolicyRequest) (*inferencev1.InferenceAccessKey, error) {
	repo, tenant, err := s.policyRepository(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || !validPolicyUUID(req.GetKeyId()) || !validPolicyUUID(req.GetPolicyId()) {
		return nil, status.Error(codes.InvalidArgument, "valid key_id and policy_id required")
	}
	actor, err := policyRequestActor(ctx, req.GetRequestId())
	if err != nil {
		return nil, err
	}
	item, err := repo.BindAccessPolicy(ctx, tenant, req.GetKeyId(), req.GetPolicyId(), actor, req.GetRequestId())
	if err != nil {
		return nil, policyStatus(err)
	}
	return accessKeyProto(item), nil
}
