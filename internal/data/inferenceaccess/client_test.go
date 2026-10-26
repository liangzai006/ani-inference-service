package inferenceaccess

import (
	"context"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/service/gatewayauthz"
	"google.golang.org/grpc"
)

type accessKeyClientStub struct {
	inferencev1.InferenceAccessKeyServiceClient
	request *inferencev1.CheckInferenceAccessRequest
}

func (s *accessKeyClientStub) CheckInferenceAccess(_ context.Context, req *inferencev1.CheckInferenceAccessRequest, _ ...grpc.CallOption) (*inferencev1.CheckInferenceAccessResponse, error) {
	s.request = req
	return &inferencev1.CheckInferenceAccessResponse{Allowed: true, TenantId: "tenant-a", KeyId: "key-a", PolicyVersion: 4}, nil
}

func TestNewClientDelegatesCheck(t *testing.T) {
	stub := &accessKeyClientStub{}
	client, err := NewClient(stub, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), gatewayauthz.CheckRequest{APIKey: "iak_secret", ServiceID: "service-a", ServedModelName: "model-a", RequestID: "request-a", RequestCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed || result.TenantID != "tenant-a" || result.InferenceKeyID != "key-a" || result.PolicyVersion != 4 {
		t.Fatalf("result = %+v", result)
	}
	if stub.request.GetApiKey() != "iak_secret" || stub.request.GetServiceId() != "service-a" || stub.request.GetServedModelName() != "model-a" || stub.request.GetRequestId() != "request-a" {
		t.Fatalf("request = %+v", stub.request)
	}
}

func TestConfigValidateRequiresMutualTLSIdentity(t *testing.T) {
	cfg := Config{Endpoint: "inference:19090", Timeout: time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a client without CA, certificate, key, and identities")
	}
	cfg.TLS = TLSConfig{CAFile: "ca.crt", CertFile: "tls.crt", KeyFile: "tls.key", ServerName: "ani-inference.ani.internal", ClientIdentity: "ani-inference-gateway.ani.internal"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected complete config: %v", err)
	}
}
