package server

import (
	"context"
	"testing"
	"time"

	kratosmetadata "github.com/go-kratos/kratos/v3/metadata"
	"github.com/go-kratos/kratos/v3/middleware"
	kratosmetadatamiddleware "github.com/go-kratos/kratos/v3/middleware/metadata"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	inferenceservice "github.com/zhangzhe-ctrl/ani-inference-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type tenantProbeCreate struct {
	tenant string
}

func (s *tenantProbeCreate) Create(ctx context.Context, in inferenceservice.CreateInput) (*inferencev1.OperationResponse, error) {
	var ok bool
	s.tenant, ok = inferenceservice.TenantID(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "tenant identity is required")
	}
	if in.TenantID != s.tenant {
		return nil, status.Error(codes.PermissionDenied, "tenant was not propagated")
	}
	return &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: s.tenant}}, nil
}

func TestDirectTenantMiddlewareBindsRequestMetadata(t *testing.T) {
	handler := DirectTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		tenant, ok := inferenceservice.TenantID(ctx)
		if !ok || tenant != "tenant-a" {
			t.Fatalf("tenant = %q, ok=%v; want tenant-a", tenant, ok)
		}
		return "ok", nil
	})

	ctx := kratosmetadata.NewServerContext(context.Background(), kratosmetadata.New(map[string][]string{
		"tenant-id": {"tenant-a"},
	}))
	got, err := handler(ctx, struct{}{})
	if err != nil || got != "ok" {
		t.Fatalf("handler() = (%v, %v), want (ok, nil)", got, err)
	}
}

func TestDirectTenantMiddlewareBindsGRPCIncomingMetadata(t *testing.T) {
	handler := DirectTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		tenant, ok := inferenceservice.TenantID(ctx)
		if !ok || tenant != "tenant-a" {
			t.Fatalf("tenant = %q, ok=%v; want tenant-a", tenant, ok)
		}
		return nil, nil
	})
	ctx := grpcmetadata.NewIncomingContext(context.Background(), grpcmetadata.Pairs("x-tenant-id", "tenant-a"))
	if _, err := handler(ctx, struct{}{}); err != nil {
		t.Fatalf("handler() error = %v", err)
	}
}

func TestDirectTenantMiddlewareRejectsMismatchWithExplicitTenant(t *testing.T) {
	handler := DirectTenantMiddleware()(func(context.Context, interface{}) (interface{}, error) {
		t.Fatal("next handler should not run")
		return nil, nil
	})
	ctx := kratosmetadata.NewServerContext(
		inferenceservice.WithTenantID(context.Background(), "tenant-a"),
		kratosmetadata.New(map[string][]string{"tenant-id": {"tenant-b"}}),
	)
	_, err := handler(ctx, struct{}{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("error code = %v, want PermissionDenied; err=%v", status.Code(err), err)
	}
}

func TestDirectTenantMiddlewareLeavesMissingMetadataForService(t *testing.T) {
	called := false
	handler := DirectTenantMiddleware()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		called = true
		if _, ok := inferenceservice.TenantID(ctx); ok {
			t.Fatal("tenant unexpectedly bound")
		}
		return nil, nil
	})
	if _, err := handler(context.Background(), struct{}{}); err != nil || !called {
		t.Fatalf("handler() = (%v, called=%v), want nil and called", err, called)
	}
}

var _ middleware.Middleware = DirectTenantMiddleware()

func TestDirectTenantMiddlewareWorksThroughGRPC(t *testing.T) {
	grpcServer := kratosgrpc.NewServer(
		kratosgrpc.Network("tcp"),
		kratosgrpc.Address("127.0.0.1:0"),
		kratosgrpc.Timeout(time.Second),
		kratosgrpc.Middleware(
			kratosmetadatamiddleware.Server(),
			DirectTenantMiddleware(),
		),
		kratosgrpc.DisableReflection(),
	)
	probe := &tenantProbeCreate{}
	inferencev1.RegisterInferenceServiceManagerServer(grpcServer, inferenceservice.NewInferenceServer(probe))
	endpoint, err := grpcServer.Endpoint()
	if err != nil {
		t.Fatalf("Endpoint() error = %v", err)
	}
	startErr := make(chan error, 1)
	go func() { startErr <- grpcServer.Start(context.Background()) }()
	t.Cleanup(func() {
		_ = grpcServer.Stop(context.Background())
		select {
		case <-startErr:
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer conn.Close()
	callCtx := grpcmetadata.NewOutgoingContext(ctx, grpcmetadata.Pairs("tenant-id", "tenant-a"))
	out, err := inferencev1.NewInferenceServiceManagerClient(conn).CreateInferenceService(callCtx, &inferencev1.CreateInferenceServiceRequest{RequestId: "request-1", Name: "service", ModelVersionId: "version", Replicas: 1, Engine: &inferencev1.EngineSpec{Type: "vllm", Image: "engine:v1", Command: []string{"serve", "/models"}}})
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if out.GetOperation().GetId() != "tenant-a" || probe.tenant != "tenant-a" {
		t.Fatalf("tenant = %q, probe tenant = %q; want tenant-a", out.GetOperation().GetId(), probe.tenant)
	}
}
