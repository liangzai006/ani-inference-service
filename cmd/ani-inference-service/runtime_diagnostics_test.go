package main

import (
	"context"
	"crypto/tls"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"testing"
)

type diagnosticTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *diagnosticTestStream) Context() context.Context { return s.ctx }
func TestRuntimeLogStreamAuthenticatesMethodIdentityAndTenant(t *testing.T) {
	const method = "/inference.v1.InferenceDiagnostics/StreamRuntimeLogs"
	for _, test := range []struct {
		name, identity, tenant, actor, method string
		code                                  codes.Code
	}{
		{"governance", "ani-governance", "tenant-a", "governance:user:7", method, codes.OK},
		{"model", "ani-model.ani.internal", "tenant-a", "model", method, codes.PermissionDenied},
		{"missing-tenant", "ani-governance", "", "governance:user:7", method, codes.Unauthenticated},
		{"missing-actor", "ani-governance", "tenant-a", "", method, codes.Unauthenticated},
		{"unknown-method", "ani-governance", "tenant-a", "governance:user:7", "/unknown", codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := governancePeerContext(tls.VersionTLS13, test.identity, true)
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-ani-tenant-id", test.tenant, "x-ani-actor", test.actor))
			called := false
			err := trustedDiagnosticsStreamInterceptor()(nil, &diagnosticTestStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: test.method}, func(_ any, stream grpc.ServerStream) error {
				called = true
				tenant, ok := service.TenantID(stream.Context())
				if !ok || tenant != test.tenant {
					t.Fatal("trusted stream tenant missing")
				}
				return nil
			})
			if status.Code(err) != test.code || called != (test.code == codes.OK) {
				t.Fatalf("code=%v called=%v err=%v", status.Code(err), called, err)
			}
		})
	}
}
