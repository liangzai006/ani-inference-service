package main

import (
	"context"
	"errors"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// trustedTenantMiddleware binds the tenant and actor supplied by an
// authenticated internal service. The ordinary gRPC listener is mTLS-only;
// metadata is accepted only after the caller certificate has been verified
// and its DNS identity is one of the identities allowed by the RPC policy.
// Request body fields never establish tenant scope.
func trustedTenantMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			identity, err := extractClientIdentity(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Unauthenticated, "trusted service identity unavailable: %v", err)
			}
			if identity != "ani-governance.ani.internal" && identity != "ani-model.ani.internal" {
				return nil, status.Errorf(codes.PermissionDenied, "unregistered trusted service %q", identity)
			}
			tenant, err := trustedTenantMetadata(ctx)
			if err != nil {
				if errors.Is(err, errTenantMetadataMissing) {
					return nil, status.Error(codes.Unauthenticated, err.Error())
				}
				return nil, status.Error(codes.InvalidArgument, err.Error())
			}
			ctx = service.WithTenantID(ctx, tenant)
			actor, actorErr := trustedActorMetadata(ctx)
			if actorErr != nil {
				return nil, status.Error(codes.InvalidArgument, actorErr.Error())
			}
			if identity == "ani-governance.ani.internal" {
				if actor == "" {
					return nil, status.Error(codes.Unauthenticated, "x-ani-actor metadata is required for Governance")
				}
				if !strings.HasPrefix(actor, "governance:") {
					return nil, status.Error(codes.PermissionDenied, "x-ani-actor must identify Governance")
				}
			}
			if actor != "" {
				ctx = service.WithActor(ctx, actor)
			}
			return next(ctx, req)
		}
	}
}

var errTenantMetadataMissing = errors.New("x-ani-tenant-id metadata is required")

func trustedTenantMetadata(ctx context.Context) (string, error) {
	md, ok := grpcmetadata.FromIncomingContext(ctx)
	if !ok {
		return "", errTenantMetadataMissing
	}
	var tenant string
	for _, key := range []string{"x-ani-tenant-id", "tenant-id", "x-tenant-id", "x-md-tenant-id"} {
		for _, value := range md.Get(key) {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if tenant == "" {
				tenant = value
				continue
			}
			if tenant != value {
				return "", errors.New("conflicting tenant metadata")
			}
		}
	}
	if tenant == "" {
		return "", errTenantMetadataMissing
	}
	return tenant, nil
}

func trustedActorMetadata(ctx context.Context) (string, error) {
	md, ok := grpcmetadata.FromIncomingContext(ctx)
	if !ok {
		return "", nil
	}
	var actor string
	for _, value := range md.Get("x-ani-actor") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if actor == "" {
			actor = value
			continue
		}
		if actor != value {
			return "", errors.New("conflicting actor metadata")
		}
	}
	return actor, nil
}
