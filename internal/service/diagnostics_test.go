package service

import (
	"context"
	api "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

type diagnosticRead struct {
	inferencebiz.ReadUseCase
	tenant, id string
}

func (r *diagnosticRead) GetService(_ context.Context, tenant, id string) (*api.InferenceService, error) {
	r.tenant, r.id = tenant, id
	return &api.InferenceService{Id: id, Generation: 7, InvocationHealth: "unknown", PublicationPhase: "published", ObservedAt: timestamppb.Now()}, nil
}

type diagnosticRuntime struct {
	biz.Reader
	scope biz.Scope
}

func (r *diagnosticRuntime) Instances(_ context.Context, scope biz.Scope) ([]biz.Instance, error) {
	r.scope = scope
	return nil, nil
}
func TestDiagnosticsTrustedScopeAndTruthfulHealth(t *testing.T) {
	read, runtime := &diagnosticRead{}, &diagnosticRuntime{}
	server := &DiagnosticsServer{Read: read, Runtime: runtime}
	_, err := server.GetRuntimeDiagnostics(context.Background(), &api.GetRuntimeDiagnosticsRequest{ResourceId: "service"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity: %v", err)
	}
	response, err := server.GetRuntimeDiagnostics(WithTenantID(context.Background(), "tenant"), &api.GetRuntimeDiagnosticsRequest{ResourceId: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if read.tenant != "tenant" || runtime.scope.TenantID != "tenant" || runtime.scope.ServiceID != "service" || runtime.scope.Generation != 7 {
		t.Fatalf("untrusted scope: %+v", runtime.scope)
	}
	if response.InvocationHealth != "unknown" || response.HealthReason != "invocation_probe_not_configured" || response.HealthObservedAt != nil || response.RuntimeObservedAt == nil {
		t.Fatalf("readiness fabricated invocation evidence: %+v", response)
	}
}
func TestDiagnosticQueryValidation(t *testing.T) {
	for _, req := range []*api.RuntimeLogsRequest{{}, {Instance: "pod", TailLines: -1}, {Instance: "pod", TailLines: 2001}, {Instance: "pod", Since: timestamppb.Now(), Until: timestamppb.New(time.Now().Add(-time.Hour))}} {
		if _, err := logQuery(req, false); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid log query accepted: %+v %v", req, err)
		}
	}
}

func TestLiveLogDeadlineStopsBlockedReceiver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	result := make(chan error, 1)
	go func() {
		result <- sendRuntimeLog(ctx, func(*api.RuntimeLogLine) error { close(started); <-release; return nil }, &api.RuntimeLogLine{})
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked receiver outlived cancellation")
	}
}
