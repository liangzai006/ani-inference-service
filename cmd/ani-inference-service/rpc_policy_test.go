package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type accessCheckTransport struct{}

func (accessCheckTransport) Kind() transport.Kind            { return transport.KindGRPC }
func (accessCheckTransport) Endpoint() string                { return "grpc://inference" }
func (accessCheckTransport) Operation() string               { return checkInferenceAccessMethod }
func (accessCheckTransport) RequestHeader() transport.Header { return nil }
func (accessCheckTransport) ReplyHeader() transport.Header   { return nil }

func TestGovernancePolicyIncludesNonGPULifecycleReads(t *testing.T) {
	policy := InferenceServicePolicy()
	for _, method := range []string{
		"/inference.v1.InferenceServiceManager/CreateInferenceService",
		"/inference.v1.InferenceServiceManager/GetInferenceService",
		"/inference.v1.InferenceServiceManager/ListInferenceServices",
		"/inference.v1.InferenceServiceManager/DeleteInferenceService",
		"/inference.v1.InferenceServiceManager/UpdateInferenceService",
		"/inference.v1.InferenceServiceManager/StartInferenceService",
		"/inference.v1.InferenceServiceManager/StopInferenceService",
		"/inference.v1.InferenceServiceManager/RestartInferenceService",
		"/inference.v1.InferenceServiceManager/GetOperation",
		"/inference.v1.InferenceServiceManager/ListOperations",
		"/inference.v1.InferenceAccessKeyService/CreateInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/ListInferenceAccessKeys",
		"/inference.v1.InferenceAccessKeyService/GetInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/RevokeInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/RotateInferenceAccessKey",
	} {
		if !policy.IsAllowed("ani-governance", method) {
			t.Fatalf("Governance policy denied %s", method)
		}
		if policy.IsAllowed("ani-governance.ani.internal", method) {
			t.Fatalf("Governance policy accepted retired identity for %s", method)
		}
	}
	if policy.IsAllowed("ani-governance", "/inference.v1.InferenceServiceManager/UnknownMethod") {
		t.Fatal("unknown method accepted")
	}
}

func TestGatewayPolicyOnlyAllowsAccessCheck(t *testing.T) {
	policy := InferenceServicePolicy()
	if !policy.IsAllowed(gatewayAuthzCallerDNS, checkInferenceAccessMethod) {
		t.Fatal("gateway authz identity denied CheckInferenceAccess")
	}
	for _, method := range []string{
		"/inference.v1.InferenceAccessKeyService/CreateInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/ListInferenceAccessKeys",
		"/inference.v1.InferenceAccessKeyService/GetInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/RevokeInferenceAccessKey",
		"/inference.v1.InferenceAccessKeyService/RotateInferenceAccessKey",
		"/inference.v1.InferenceServiceManager/GetInferenceService",
	} {
		if policy.IsAllowed(gatewayAuthzCallerDNS, method) {
			t.Fatalf("gateway authz identity accepted %s", method)
		}
	}
	if policy.IsAllowed(governanceCallerDNS, checkInferenceAccessMethod) {
		t.Fatal("Governance identity accepted gateway-only CheckInferenceAccess")
	}
}

func governancePeerContext(version uint16, dns string, verified bool) context.Context {
	leaf := &x509.Certificate{DNSNames: []string{dns}}
	state := tls.ConnectionState{Version: version, PeerCertificates: []*x509.Certificate{leaf}}
	if verified {
		state.VerifiedChains = [][]*x509.Certificate{{leaf}}
	}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
}

func TestTrustedTenantMiddlewareBindsGovernanceMetadata(t *testing.T) {
	ctx := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "tenant-a", "x-ani-actor", "governance:user:7"))
	called := false
	handler := trustedTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		called = true
		if tenant, ok := service.TenantID(ctx); !ok || tenant != "tenant-a" {
			t.Fatalf("tenant=%q present=%v", tenant, ok)
		}
		if got := service.Actor(ctx); got != "governance:user:7" {
			t.Fatalf("actor=%q", got)
		}
		return nil, nil
	})
	if _, err := handler(ctx, struct{}{}); err != nil || !called {
		t.Fatalf("trusted metadata rejected: called=%v err=%v", called, err)
	}
}

func TestTrustedTenantMiddlewareAllowsGatewayCheckWithoutTenant(t *testing.T) {
	ctx := governancePeerContext(tls.VersionTLS13, gatewayAuthzCallerDNS, true)
	ctx = transport.NewServerContext(ctx, accessCheckTransport{})
	called := false
	handler := trustedTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		called = true
		if _, ok := service.TenantID(ctx); ok {
			t.Fatal("gateway check unexpectedly received tenant context")
		}
		return nil, nil
	})
	if _, err := handler(ctx, &inferencev1.CheckInferenceAccessRequest{ApiKey: "iak_test"}); err != nil || !called {
		t.Fatalf("gateway check rejected: called=%v err=%v", called, err)
	}
}

func TestTrustedTenantMiddlewareRejectsGatewayTenantMetadata(t *testing.T) {
	ctx := governancePeerContext(tls.VersionTLS13, gatewayAuthzCallerDNS, true)
	ctx = transport.NewServerContext(ctx, accessCheckTransport{})
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "tenant-a"))
	handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("gateway check with tenant metadata reached handler")
		return nil, nil
	})
	if _, err := handler(ctx, &inferencev1.CheckInferenceAccessRequest{ApiKey: "iak_test"}); status.Code(err) != 7 {
		t.Fatalf("code=%v err=%v; want PermissionDenied", status.Code(err), err)
	}
}

func TestTrustedTenantMiddlewareMarksGPUCommandsOnTheOrdinaryListener(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	request := &inferencev1.CreateInferenceServiceRequest{Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{}}, Context: &inferencev1.CommandContext{TenantId: tenant, ResourceId: "10000000-0000-4000-8000-000000000002", OperationId: "10000000-0000-4000-8000-000000000003", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	ctx := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", "governance:user:7"))
	marked := false
	handler := trustedTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		marked = gpu.GovernanceIdentity(ctx)
		return nil, nil
	})
	if _, err := handler(ctx, request); err != nil {
		t.Fatal(err)
	}
	if !marked {
		t.Fatal("GPU command was not marked")
	}
}

func TestTrustedTenantMiddlewareRejectsGPUContextTenantMismatch(t *testing.T) {
	request := &inferencev1.CreateInferenceServiceRequest{Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{}}, Context: &inferencev1.CommandContext{TenantId: "10000000-0000-4000-8000-000000000001", ResourceId: "10000000-0000-4000-8000-000000000002", OperationId: "10000000-0000-4000-8000-000000000003", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	ctx := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "10000000-0000-4000-8000-000000000004", "x-ani-actor", "governance:user:7"))
	handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("mismatched context reached handler")
		return nil, nil
	})
	if _, err := handler(ctx, request); status.Code(err) != 7 {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestTrustedTenantMiddlewareRejectsMissingOrConflictingMetadata(t *testing.T) {
	base := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("unauthenticated request reached handler")
		return nil, nil
	})
	for name, ctx := range map[string]context.Context{
		"missing": base,
		"conflicting": metadata.NewIncomingContext(base, metadata.Pairs(
			"x-ani-tenant-id", "tenant-a", "tenant-id", "tenant-b",
		)),
		"missing-actor": metadata.NewIncomingContext(base, metadata.Pairs("x-ani-tenant-id", "tenant-a")),
		"conflicting-actor": metadata.NewIncomingContext(base, metadata.Pairs(
			"x-ani-tenant-id", "tenant-a", "x-ani-actor", "governance:user:7", "x-ani-actor", "governance:user:8",
		)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := handler(ctx, struct{}{}); err == nil {
				t.Fatal("invalid tenant metadata accepted")
			} else if status.Code(err) != 16 && status.Code(err) != 3 {
				t.Fatalf("code=%v err=%v", status.Code(err), err)
			}
		})
	}
}

func TestInferenceServerTLSConfigRequiresAllFiles(t *testing.T) {
	for _, cfg := range []InferenceServerTLSConfig{
		{},
		{CAFile: "ca.crt", CertFile: "tls.crt"},
		{CAFile: "ca.crt", KeyFile: "tls.key"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("incomplete TLS config accepted: %+v", cfg)
		}
	}
}

var _ middleware.Middleware = trustedTenantMiddleware()

func TestTrustedTenantMiddlewareRejectsWrongIdentity(t *testing.T) {
	ctx := governancePeerContext(tls.VersionTLS13, "evil.ani.internal", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "tenant-a"))
	handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("wrong identity reached handler")
		return nil, nil
	})
	if _, err := handler(ctx, struct{}{}); err == nil {
		t.Fatal("wrong identity accepted")
	}
}

func TestTrustedTenantMiddlewareAcceptsOnlyVerifiedTLS13(t *testing.T) {
	for _, test := range []struct {
		name    string
		version uint16
		verify  bool
		wantErr bool
	}{
		{name: "tls13", version: tls.VersionTLS13, verify: true},
		{name: "tls12", version: tls.VersionTLS12, verify: true, wantErr: true},
		{name: "unverified", version: tls.VersionTLS13, verify: false, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := governancePeerContext(test.version, "ani-governance", test.verify)
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "tenant-a", "x-ani-actor", "governance:user:7"))
			handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) { return nil, nil })
			_, err := handler(ctx, struct{}{})
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestServicePolicyRPCIdentityBoundary(t *testing.T) {
	policy := InferenceServicePolicy()
	for _, method := range []string{"/inference.v1.InferenceServiceManager/GetServicePolicy", "/inference.v1.InferenceServiceManager/UpdateServicePolicy"} {
		if !policy.IsAllowed(governanceCallerDNS, method) {
			t.Fatalf("governance cannot call %s", method)
		}
		for _, identity := range []string{gatewayAuthzCallerDNS, "ani-model.ani.internal", "unregistered"} {
			if policy.IsAllowed(identity, method) {
				t.Fatalf("%s can call %s", identity, method)
			}
		}
	}
}
