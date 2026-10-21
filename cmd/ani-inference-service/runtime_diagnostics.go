package main

import (
	"context"
	"github.com/go-kratos/kratos/v3/middleware"
	kratosTransport "github.com/go-kratos/kratos/v3/transport"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type diagnosticsManagerServer struct {
	kratosTransport.Server
	reader biz.Reader
}

func (s *diagnosticsManagerServer) DiagnosticsReader() biz.Reader { return s.reader }

type trustedDiagnosticStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *trustedDiagnosticStream) Context() context.Context { return s.ctx }

// Kratos unary middleware does not authorize streaming methods. Apply the same
// certificate, metadata and exact method policy before a streaming handler runs.
func trustedDiagnosticsStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		identity, err := extractClientIdentity(stream.Context())
		if err != nil {
			return status.Error(codes.Unauthenticated, "trusted service identity unavailable")
		}
		if !InferenceServicePolicy().IsAllowed(identity, info.FullMethod) {
			return status.Error(codes.PermissionDenied, "stream method not authorized")
		}
		var authenticated context.Context
		bind := trustedTenantMiddleware()(middleware.Handler(func(ctx context.Context, _ any) (any, error) { authenticated = ctx; return nil, nil }))
		if _, err := bind(stream.Context(), nil); err != nil {
			return err
		}
		return handler(srv, &trustedDiagnosticStream{ServerStream: stream, ctx: authenticated})
	}
}
