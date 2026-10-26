package main

import (
	"context"
	"errors"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
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
			if identity != governanceCallerDNS && identity != "ani-model.ani.internal" {
				if identity != gatewayAuthzCallerDNS {
					return nil, status.Errorf(codes.PermissionDenied, "unregistered trusted service %q", identity)
				}
				tr, ok := transport.FromServerContext(ctx)
				if !ok || tr.Operation() != checkInferenceAccessMethod {
					return nil, status.Errorf(codes.PermissionDenied, "gateway identity is not authorized for this method")
				}
				if hasTrustedTenantOrActorMetadata(ctx) {
					return nil, status.Error(codes.PermissionDenied, "gateway authz calls must not supply tenant or actor metadata")
				}
				return next(ctx, req)
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
			if identity == governanceCallerDNS {
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
			var bindErr error
			ctx, bindErr = bindGovernanceGPUContext(ctx, req, tenant, identity)
			if bindErr != nil {
				return nil, bindErr
			}
			return next(ctx, req)
		}
	}
}

func hasTrustedTenantOrActorMetadata(ctx context.Context) bool {
	md, ok := grpcmetadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, key := range []string{"x-ani-tenant-id", "tenant-id", "x-tenant-id", "x-md-tenant-id", "x-ani-actor"} {
		for _, value := range md.Get(key) {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

// bindGovernanceGPUContext keeps the single ordinary listener usable for both
// tenant-scoped reads and Governance-owned GPU commands. The mTLS identity and
// tenant metadata are trusted first; the attachment tenant is only a
// consistency check and never establishes scope by itself.
func bindGovernanceGPUContext(ctx context.Context, req interface{}, tenant, identity string) (context.Context, error) {
	if identity != governanceCallerDNS {
		return ctx, nil
	}
	var attachmentTenant string
	switch request := req.(type) {
	case *inferencev1.CreateInferenceServiceRequest:
		if request.GetContext() != nil {
			attachmentTenant = strings.TrimSpace(request.GetContext().GetTenantId())
		}
	case *inferencev1.DeleteInferenceServiceRequest:
		if request.GetContext() != nil {
			attachmentTenant = strings.TrimSpace(request.GetContext().GetTenantId())
		}
	case *inferencev1.UpdateInferenceServiceRequest:
		if request.GetContext() != nil {
			attachmentTenant = strings.TrimSpace(request.GetContext().GetTenantId())
		}
	}
	if attachmentTenant == "" {
		return ctx, nil
	}
	if attachmentTenant != tenant {
		return ctx, status.Error(codes.PermissionDenied, "GPU command tenant does not match authenticated scope")
	}
	return gpu.WithGovernanceIdentity(ctx), nil
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
