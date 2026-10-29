package service

import (
	"context"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The service-bound policy API is retired. Its wire methods remain reserved
// for old clients, but cannot read or mutate policies through this server.
func (s *InferenceServer) GetServicePolicy(context.Context, *inferencev1.GetServicePolicyRequest) (*inferencev1.GetServicePolicyResponse, error) {
	return nil, status.Error(codes.Unimplemented, "service-bound policies are retired")
}
func (s *InferenceServer) UpdateServicePolicy(context.Context, *inferencev1.UpdateServicePolicyRequest) (*inferencev1.UpdateServicePolicyResponse, error) {
	return nil, status.Error(codes.Unimplemented, "service-bound policies are retired")
}
