package service

import (
	"context"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestServicePolicyTransportRetired(t *testing.T) {
	server := NewInferenceServerWithAll(nil, nil, nil, nil)
	if _, err := server.GetServicePolicy(context.Background(), &inferencev1.GetServicePolicyRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("legacy get: %v", err)
	}
	if _, err := server.UpdateServicePolicy(context.Background(), &inferencev1.UpdateServicePolicyRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("legacy update: %v", err)
	}
}
