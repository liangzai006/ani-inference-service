package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/go-kratos/kratos/v3/middleware"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/service"
	integrationv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestGovernancePolicyIncludesNonGPULifecycleReads(t *testing.T) {
	policy := InferenceServicePolicy()
	for _, method := range []string{
		"/inference.v1.InferenceServiceManager/CreateInferenceService",
		"/inference.v1.InferenceServiceManager/GetInferenceService",
		"/inference.v1.InferenceServiceManager/ListInferenceServices",
		"/inference.v1.InferenceServiceManager/DeleteInferenceService",
		"/inference.v1.InferenceServiceManager/GetOperation",
		"/inference.v1.InferenceServiceManager/ListOperations",
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

func TestTrustedTenantMiddlewareMarksGPUCommandsOnTheOrdinaryListener(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	request := &inferencev1.CreateInferenceServiceRequest{
		Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{}},
		GpuOwnerAttachment: &integrationv1.GpuOwnerCreateAttachment{
			Ref: &acceleratorv1.GpuUsageRef{TenantId: tenant},
		},
	}
	ctx := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", "governance:user:7"))
	var marked bool
	handler := trustedTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		marked = gpu.GovernanceIdentity(ctx)
		return nil, nil
	})
	if _, err := handler(ctx, request); err != nil {
		t.Fatalf("GPU command rejected on ordinary listener: %v", err)
	}
	if !marked {
		t.Fatal("GPU command did not receive Governance identity on ordinary listener")
	}
}

func TestTrustedTenantMiddlewareRejectsGPUAttachmentTenantMismatch(t *testing.T) {
	request := &inferencev1.CreateInferenceServiceRequest{
		Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{}},
		GpuOwnerAttachment: &integrationv1.GpuOwnerCreateAttachment{
			Ref: &acceleratorv1.GpuUsageRef{TenantId: "10000000-0000-4000-8000-000000000001"},
		},
	}
	ctx := governancePeerContext(tls.VersionTLS13, "ani-governance", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", "10000000-0000-4000-8000-000000000002", "x-ani-actor", "governance:user:7"))
	handler := trustedTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("mismatched GPU attachment reached handler")
		return nil, nil
	})
	if _, err := handler(ctx, request); status.Code(err) != 7 {
		t.Fatalf("code=%v err=%v want PermissionDenied", status.Code(err), err)
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
