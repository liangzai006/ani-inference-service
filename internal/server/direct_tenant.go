package server

import (
	"context"
	"errors"
	"strings"

	kratosmetadata "github.com/go-kratos/kratos/v3/metadata"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	inferenceservice "github.com/zhangzhe-ctrl/ani-inference-service/internal/service"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// DirectTenantMiddleware provides the isolated plaintext validation path with
// a tenant scope from request metadata. A trusted middleware may bind a tenant
// first; metadata must then match it exactly.
func DirectTenantMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			tenant, present, err := requestTenant(ctx)
			if err != nil {
				return nil, err
			}
			if !present {
				return next(ctx, req)
			}
			selected, err := inferenceservice.RequireTenant(ctx, tenant)
			if err != nil {
				if errors.Is(err, inferenceservice.ErrTenantMismatch) {
					return nil, status.Error(codes.PermissionDenied, err.Error())
				}
				return nil, status.Error(codes.Unauthenticated, err.Error())
			}
			if _, ok := inferenceservice.TenantID(ctx); ok {
				return next(ctx, req)
			}
			return next(inferenceservice.WithTenantID(ctx, selected), req)
		}
	}
}

func requestTenant(ctx context.Context) (string, bool, error) {
	values := make([]string, 0, 2)
	if md, ok := grpcmetadata.FromIncomingContext(ctx); ok {
		for _, key := range []string{"tenant-id", "x-tenant-id", "x-md-tenant-id"} {
			values = append(values, md.Get(key)...)
		}
	}
	if md, ok := kratosmetadata.FromServerContext(ctx); ok {
		values = append(values, md.Values("tenant-id")...)
	}
	if tr, ok := transport.FromServerContext(ctx); ok && tr.RequestHeader() != nil {
		for _, key := range []string{"tenant-id", "x-tenant-id", "x-md-tenant-id"} {
			values = append(values, tr.RequestHeader().Values(key)...)
		}
	}

	tenant := ""
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if tenant == "" {
			tenant = value
			continue
		}
		if tenant != value {
			return "", false, status.Error(codes.InvalidArgument, "conflicting request tenant metadata")
		}
	}
	return tenant, tenant != "", nil
}
