package service

import (
	"context"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	policybiz "github.com/liangzai006/ani-inference-service/internal/biz/servicepolicy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestServicePolicyTransportRejectsUntrustedInput(t *testing.T) {
	server := NewInferenceServerWithAll(nil, nil, nil, nil)
	server.SetServicePolicy(&policybiz.Service{})
	id := "11111111-1111-4111-8111-111111111111"
	ctx := WithTenantID(context.Background(), id)
	for _, tc := range []struct {
		ctx     context.Context
		request *inferencev1.UpdateServicePolicyRequest
		want    codes.Code
	}{
		{context.Background(), &inferencev1.UpdateServicePolicyRequest{ResourceId: id, Policy: &inferencev1.ServicePolicy{}}, codes.Unauthenticated},
		{ctx, nil, codes.InvalidArgument},
		{ctx, &inferencev1.UpdateServicePolicyRequest{ResourceId: id}, codes.InvalidArgument},
		{ctx, &inferencev1.UpdateServicePolicyRequest{ResourceId: id, Policy: &inferencev1.ServicePolicy{ResourceId: "different"}}, codes.InvalidArgument},
		{ctx, &inferencev1.UpdateServicePolicyRequest{ResourceId: id, Policy: &inferencev1.ServicePolicy{}}, codes.InvalidArgument},
		{metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-request-id", "one", "x-ani-request-id", "two")), &inferencev1.UpdateServicePolicyRequest{ResourceId: id, Policy: &inferencev1.ServicePolicy{}}, codes.InvalidArgument},
	} {
		_, err := server.UpdateServicePolicy(tc.ctx, tc.request)
		if status.Code(err) != tc.want {
			t.Fatalf("request=%v err=%v want=%v", tc.request, err, tc.want)
		}
	}
}
