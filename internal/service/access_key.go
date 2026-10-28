package service

import (
	"context"
	"errors"
	"strings"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InferenceAccessKeyServer is the transport adapter for Inference-owned
// data-plane credentials. It deliberately has no Governance or Kubernetes
// dependencies; tenant and actor identity come from the mTLS middleware.
type InferenceAccessKeyServer struct {
	inferencev1.UnimplementedInferenceAccessKeyServiceServer
	service *accesskey.Service
}

func NewInferenceAccessKeyServer(svc *accesskey.Service) *InferenceAccessKeyServer {
	return &InferenceAccessKeyServer{service: svc}
}

func (s *InferenceAccessKeyServer) CreateInferenceAccessKey(ctx context.Context, req *inferencev1.CreateInferenceAccessKeyRequest) (*inferencev1.CreateInferenceAccessKeyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	expiresAt, err := accessKeyTimestampValue(req.GetExpiresAt())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	result, err := s.service.Create(ctx, accesskey.CreateInput{
		TenantID: tenant, RequestID: req.GetRequestId(), Name: req.GetName(), AllowedServiceIDs: req.GetAllowedServiceIds(),
		AllowedModelNames: req.GetAllowedModelNames(), RequestsPerSecond: req.GetRequestsPerSecond(),
		RequestsPerMinute: req.GetRequestsPerMinute(), RequestsPerHour: req.GetRequestsPerHour(),
		ExpiresAt: expiresAt, CreatedBy: Actor(ctx), IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, accessKeyStatusError(err)
	}
	return &inferencev1.CreateInferenceAccessKeyResponse{Key: accessKeyProto(result.Record), Secret: result.Secret, Replayed: result.Replayed}, nil
}

func (s *InferenceAccessKeyServer) ListInferenceAccessKeys(ctx context.Context, req *inferencev1.ListInferenceAccessKeysRequest) (*inferencev1.ListInferenceAccessKeysResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	page, size := req.GetPage(), req.GetPageSize()
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || page > 100 || size < 1 || size > 100 {
		return nil, status.Error(codes.InvalidArgument, "page and page_size must be between 1 and 100")
	}
	items, total, err := s.service.List(ctx, tenant, accesskey.ListOptions{Limit: int(size), Offset: int((page - 1) * size)})
	if err != nil {
		return nil, accessKeyStatusError(err)
	}
	response := &inferencev1.ListInferenceAccessKeysResponse{Total: total, Items: make([]*inferencev1.InferenceAccessKey, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, accessKeyProto(item))
	}
	return response, nil
}

func (s *InferenceAccessKeyServer) GetInferenceAccessKey(ctx context.Context, req *inferencev1.GetInferenceAccessKeyRequest) (*inferencev1.InferenceAccessKey, error) {
	if req == nil || strings.TrimSpace(req.GetId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	item, err := s.service.Get(ctx, tenant, req.GetId())
	if err != nil {
		return nil, accessKeyStatusError(err)
	}
	return accessKeyProto(item), nil
}

func (s *InferenceAccessKeyServer) RevokeInferenceAccessKey(ctx context.Context, req *inferencev1.RevokeInferenceAccessKeyRequest) (*inferencev1.RevokeInferenceAccessKeyResponse, error) {
	if req == nil || strings.TrimSpace(req.GetId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	item, err := s.service.Revoke(ctx, tenant, req.GetId(), Actor(ctx), req.GetRequestId())
	if err != nil {
		return nil, accessKeyStatusError(err)
	}
	return &inferencev1.RevokeInferenceAccessKeyResponse{Key: accessKeyProto(item)}, nil
}

func (s *InferenceAccessKeyServer) RotateInferenceAccessKey(ctx context.Context, req *inferencev1.RotateInferenceAccessKeyRequest) (*inferencev1.RotateInferenceAccessKeyResponse, error) {
	if req == nil || strings.TrimSpace(req.GetId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if strings.TrimSpace(req.GetRequestId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	tenant, err := requireAccessKeyTenant(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	result, err := s.service.Rotate(ctx, tenant, req.GetId(), req.GetIdempotencyKey(), Actor(ctx), req.GetRequestId())
	if err != nil {
		return nil, accessKeyStatusError(err)
	}
	return &inferencev1.RotateInferenceAccessKeyResponse{Key: accessKeyProto(result.Record), Secret: result.Secret, Replayed: result.Replayed}, nil
}

func (s *InferenceAccessKeyServer) CheckInferenceAccess(ctx context.Context, req *inferencev1.CheckInferenceAccessRequest) (*inferencev1.CheckInferenceAccessResponse, error) {
	if req == nil || strings.TrimSpace(req.GetApiKey()) == "" {
		return nil, status.Error(codes.InvalidArgument, "api_key is required")
	}
	if s == nil || s.service == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference access-key service is not configured")
	}
	result, err := s.service.Check(ctx, req.GetApiKey(), req.GetServiceId(), req.GetServedModelName(), int64(req.GetRequestCost()))
	if err != nil {
		// The adapter must fail closed when the authority is unavailable.
		return nil, status.Error(codes.Unavailable, "inference access check unavailable")
	}
	response := &inferencev1.CheckInferenceAccessResponse{
		Allowed: result.Allowed, DenialCode: result.DenialCode, PolicyVersion: result.PolicyVersion,
	}
	if result.Record.ID != "" {
		response.KeyId = result.Record.ID
		response.TenantId = result.Record.TenantID
	}
	if result.RetryAfter > 0 {
		response.RetryAfterSeconds = int32((result.RetryAfter + 999*time.Millisecond) / time.Second)
	}
	return response, nil
}

func requireAccessKeyTenant(ctx context.Context) (string, error) {
	tenant, ok := TenantID(ctx)
	if !ok || strings.TrimSpace(tenant) == "" {
		return "", status.Error(codes.Unauthenticated, "tenant identity is required")
	}
	return tenant, nil
}

func accessKeyProto(item accesskey.Record) *inferencev1.InferenceAccessKey {
	result := &inferencev1.InferenceAccessKey{
		Id: item.ID, Name: item.Name, KeyPrefix: item.KeyPrefix,
		AllowedServiceIds: append([]string(nil), item.AllowedServiceIDs...),
		AllowedModelNames: append([]string(nil), item.AllowedModelNames...),
		RequestsPerSecond: item.RequestsPerSecond, RequestsPerMinute: item.RequestsPerMinute,
		RequestsPerHour: item.RequestsPerHour, CreatedBy: item.CreatedBy, Version: item.Version,
	}
	if item.Secret != "" {
		result.Secret = &item.Secret
	}
	if !item.CreatedAt.IsZero() {
		result.CreatedAt = timestamppb.New(item.CreatedAt)
	}
	if item.ExpiresAt != nil {
		result.ExpiresAt = timestamppb.New(*item.ExpiresAt)
	}
	if item.RevokedAt != nil {
		result.RevokedAt = timestamppb.New(*item.RevokedAt)
	}
	if item.LastUsedAt != nil {
		result.LastUsedAt = timestamppb.New(*item.LastUsedAt)
	}
	return result
}

func accessKeyTimestampValue(value *timestamppb.Timestamp) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.CheckValid(); err != nil {
		return nil, err
	}
	t := value.AsTime().UTC()
	return &t, nil
}

func accessKeyStatusError(err error) error {
	switch {
	case errors.Is(err, accesskey.ErrNotFound):
		return status.Error(codes.NotFound, "inference access key not found")
	case errors.Is(err, accesskey.ErrAlreadyRevoked):
		return status.Error(codes.FailedPrecondition, "inference access key is revoked")
	case errors.Is(err, accesskey.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, accesskey.ErrScopeDenied):
		return status.Error(codes.PermissionDenied, "inference access key scope denied")
	case errors.Is(err, accesskey.ErrSecretUnavailable):
		return status.Error(codes.Unavailable, "inference access key secret unavailable")
	default:
		return status.Error(codes.Internal, "inference access key persistence failed")
	}
}
