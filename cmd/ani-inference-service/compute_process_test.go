package main

// This opt-in fixture runs production acceptance, Model wrappers, PostgreSQL,
// runner, close recovery and mTLS. Only Model's RPC and external runtime/ACC
// ports are controlled. It never constructs a Kubernetes client or downloads
// an artifact; its Ready/Released values are software-test inputs only.
import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"github.com/liangzai006/ani-inference-service/internal/biz/publication"
	"github.com/liangzai006/ani-inference-service/internal/biz/quota"
	"github.com/liangzai006/ani-inference-service/internal/biz/work"
	modeldata "github.com/liangzai006/ani-inference-service/internal/data/model"
	"github.com/liangzai006/ani-inference-service/internal/data/postgres"
	"github.com/liangzai006/ani-inference-service/internal/server"
	"github.com/liangzai006/ani-inference-service/internal/service"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestComputeOwnerProcess(t *testing.T) {
	if os.Getenv("INFERENCE_COMPUTE_PROCESS") != "1" {
		t.Skip("explicit cross-repository software fixture")
	}
	pool := managedProcessPool(t)
	dir := requiredProcessEnv(t, "INFERENCE_COMPUTE_CONTROL_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute private control directory required")
	}
	raw, err := os.ReadFile(requiredProcessEnv(t, "INFERENCE_MANAGED_OWNER_CONFIG_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	var bc inferencev1.Bootstrap
	if err = protojson.Unmarshal(raw, &bc); err != nil {
		t.Fatal(err)
	}
	if bc.GetServer().GetGrpc() == nil {
		t.Fatal("mTLS listener config required")
	}
	if bc.Server.Grpc.Timeout == nil {
		bc.Server.Grpc.Timeout = durationpb.New(3 * time.Second)
	}
	repo := postgres.NewRepository(pool)
	boundary := &computeProcessBoundary{dir: dir}
	modelClient := modeldata.NewClient(boundary)
	owner := service.NewInferenceServerWithAll(modeldata.NewCreateUseCase(modelClient, postgres.NewCreateUseCase(repo)), postgres.NewReadUseCase(pool), postgres.NewCommandUseCase(repo), modeldata.NewUpdateUseCase(modelClient, postgres.NewUpdateUseCase(repo)))
	owner.SetManagedGPUClusterID(os.Getenv("INFERENCE_COMPUTE_CLUSTER_ID"))
	reporter, closeReporter, err := configuredGovernanceRefundReporter(bc.ManagedGpu, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer closeReporter()
	boundary.reporter = reporter
	coordinator := &service.ManagedGPUReleaseCoordinator{Observer: boundary, Contexts: repo, Reporter: boundary}
	store := postgres.NewWorkStore(pool, postgres.WorkStoreOptions{Owner: "compute-software-fixture", LeaseSeconds: 2, ObserveSeconds: .01, RetrySeconds: .01})
	runner := &inferencebiz.Runner{Store: postgres.NewOperationStore(pool), Admission: boundary, Model: boundary, GPU: boundary, Quota: boundary, Publication: boundary, Runtime: boundary, ManagedGPURelease: coordinator, RetryAfter: 10 * time.Millisecond}
	worker := &work.Worker{Store: store, Execute: runner.Execute}
	loop := &work.Loop{Worker: worker, Tenants: store, Close: func(ctx context.Context) error { return repo.RunManagedCloses(ctx, coordinator) }}
	listener, err := configuredInferenceGRPCServer(bc.Server.Grpc, server.DirectTenantMiddleware())
	if err != nil {
		t.Fatal(err)
	}
	inferencev1.RegisterInferenceServiceManagerServer(listener, owner)
	endpoint, err := listener.Endpoint()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- listener.Start(ctx) }()
	boundary.event("process_started", nil)
	if err = os.WriteFile(requiredProcessEnv(t, "INFERENCE_MANAGED_READY"), []byte(endpoint.Host), 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err = <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-ctx.Done():
			shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
			err = listener.Stop(shutdown)
			stop()
			if err != nil {
				t.Error(err)
			}
			select {
			case err = <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("owner listener did not stop")
			}
			boundary.event("process_stopped", nil)
			return
		case <-ticker.C:
			if boundary.flag("pause") {
				continue
			}
			if err = loop.RunOnce(ctx); err != nil && ctx.Err() == nil {
				boundary.event("loop_retry", map[string]string{"error": err.Error()})
			}
		}
	}
}

type computeProcessBoundary struct {
	modelv1.ModelServiceClient
	dir      string
	mu       sync.Mutex
	reporter gpu.RefundReporter
}

func (p *computeProcessBoundary) flag(name string) bool {
	_, err := os.Stat(filepath.Join(p.dir, name))
	return err == nil
}
func (p *computeProcessBoundary) event(name string, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(p.dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(struct {
		Name  string `json:"name"`
		Value any    `json:"value,omitempty"`
	}{name, value}); err != nil {
		panic(err)
	}
}
func (p *computeProcessBoundary) GetModelVersion(_ context.Context, r *modelv1.GetModelVersionRequest, _ ...grpc.CallOption) (*modelv1.GetModelVersionResponse, error) {
	p.event("model_get", map[string]string{"tenant": r.TenantId, "version": r.ModelVersionId})
	if p.flag("model-unavailable") {
		return nil, status.Error(codes.Unavailable, "controlled external Model failure")
	}
	return &modelv1.GetModelVersionResponse{Model: &modelv1.Model{Id: "software-model", TenantId: r.TenantId, Capabilities: []string{"text-generation"}}, Version: &modelv1.ModelVersion{Id: r.ModelVersionId, ModelId: "software-model", Status: "ready", StoragePath: "isolated/software/model", ChecksumSha256: strings.Repeat("a", 64), SizeBytes: 1}}, nil
}

func (p *computeProcessBoundary) AbortTrainedArtifact(context.Context, *modelv1.AbortTrainedArtifactRequest, ...grpc.CallOption) (*modelv1.AbortTrainedArtifactResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AbortTrainedArtifact is outside this compute fixture")
}
func (p *computeProcessBoundary) Admit(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("admit", o.ID)
	return nil
}
func (p *computeProcessBoundary) EnsureModel(_ context.Context, o inferencebiz.OperationContext) (inferencebiz.ModelObservation, error) {
	p.event("runtime_model_ready", o.ID)
	return inferencebiz.ModelObservation{Known: true, Ready: true}, nil
}
func (p *computeProcessBoundary) CleanupModel(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("cleanup_model", o.ID)
	if p.flag("cleanup-model-fails") {
		return errors.New("controlled model cleanup failure")
	}
	return nil
}
func (p *computeProcessBoundary) ApplyCR(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("apply_cr", o)
	return nil
}
func (p *computeProcessBoundary) ApplyRuntime(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("apply_runtime", o)
	return nil
}
func (p *computeProcessBoundary) ObserveRuntime(_ context.Context, o inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	p.event("observe_runtime", o.ID)
	return inferencebiz.RuntimeObservation{Ready: true, RuntimePhase: "ready", ReadyReplicas: o.Replicas, ModelReadyKnown: true, ModelReady: true}, nil
}
func (p *computeProcessBoundary) DeleteRuntime(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("delete_runtime", o.ID)
	return nil
}
func (p *computeProcessBoundary) ObserveAbsence(_ context.Context, o inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	p.event("observe_absence", o.ID)
	return inferencebiz.RuntimeObservation{Absent: true}, nil
}
func (p *computeProcessBoundary) DeleteCR(_ context.Context, o inferencebiz.OperationContext) error {
	p.event("delete_cr", o.ID)
	return nil
}
func (p *computeProcessBoundary) Withdraw(_ context.Context, v publication.Publication) error {
	p.event("withdraw", v.OperationID)
	return nil
}
func (p *computeProcessBoundary) ConfirmWithdrawn(_ context.Context, v publication.Publication) (bool, error) {
	p.event("withdrawn", v.OperationID)
	return true, nil
}
func (p *computeProcessBoundary) Publish(_ context.Context, v publication.Publication) error {
	p.event("publish", v.OperationID)
	return nil
}
func (p *computeProcessBoundary) ConfirmPublished(_ context.Context, v publication.Publication) (bool, error) {
	p.event("published", v.OperationID)
	return true, nil
}
func (p *computeProcessBoundary) Endpoint(context.Context, publication.Publication) (string, error) {
	return "http://software-runtime.invalid/v1", nil
}
func (p *computeProcessBoundary) Reserve(_ context.Context, v quota.Reservation) (quota.Reservation, error) {
	p.event("legacy_reserve", v.OperationID)
	return quota.Reservation{}, errors.New("managed resource reached old quota")
}
func (p *computeProcessBoundary) Confirm(_ context.Context, v quota.Reservation) error {
	p.event("legacy_confirm", v.OperationID)
	return errors.New("managed resource reached old quota")
}
func (p *computeProcessBoundary) Release(_ context.Context, v quota.Reservation) error {
	p.event("legacy_release", v.OperationID)
	return errors.New("managed resource reached old quota")
}
func (p *computeProcessBoundary) Get(context.Context, string, string) (quota.Reservation, error) {
	p.event("legacy_get", nil)
	return quota.Reservation{}, errors.New("managed resource reached old quota")
}
func (p *computeProcessBoundary) ResolveGPU(_ context.Context, o inferencebiz.OperationContext) (*gpu.Snapshot, error) {
	p.event("gpu_resolve", o.ID)
	return nil, errors.New("managed resource reached owner GPU resolver")
}
func (p *computeProcessBoundary) ObserveRelease(_ context.Context, in gpu.ReleaseObservationInput) (gpu.ReleaseObservation, error) {
	p.event("observe_release", in)
	var result gpu.ReleaseObservation
	raw, err := os.ReadFile(filepath.Join(p.dir, "acc-result.json"))
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	return result, nil
}
func (p *computeProcessBoundary) ReportResourceReleased(ctx context.Context, n gpu.ReleaseNotification) error {
	p.event("release_report", n)
	if p.reporter == nil {
		return errors.New("real Gov reporter required")
	}
	if err := p.reporter.ReportResourceReleased(ctx, n); err != nil {
		return err
	}
	if p.flag("lose-release-ack-once") {
		if err := os.Rename(filepath.Join(p.dir, "lose-release-ack-once"), filepath.Join(p.dir, "lost-release-ack")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(p.dir, "pause"), nil, 0600); err != nil {
			return err
		}
		p.event("release_applied_ack_lost", n)
		return errors.New("Gov processed; controlled reply loss before owner completion")
	}
	return nil
}
