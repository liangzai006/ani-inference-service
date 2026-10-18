package postgres

// These tests exercise the managed GPU command path with the accelerator
// main contract. They run against PostgreSQL when INFERENCE_PG_DSN is set;
// lifecycleDatabase skips them in a unit-only checkout.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/data/kubernetes"
	"github.com/liangzai006/ani-inference-service/internal/service"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func managedCreateFixture(t *testing.T, tenant, resource uuid.UUID) *inferencev1.CreateInferenceServiceRequest {
	t.Helper()
	cluster, pool, profile := uuid.New(), uuid.New(), uuid.New()
	request := &inferencev1.GpuRequest{ClusterId: cluster.String(), PoolId: pool.String(), ProfileId: profile.String(), ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	execution := &acceleratorv1.GpuExecution{
		SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia",
		NodeSelector:   map[string]string{"accelerator.ani.io/supply-group": pool.String(), "accelerator.ani.io/model-key": "nvidia-test", "accelerator.ani.io/baseline-id": profile.String()},
		PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"},
		Resources:      map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "6144", "volcano.sh/vgpu-cores": "25"},
	}
	var err error
	execution.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(execution)
	if err != nil {
		t.Fatal(err)
	}
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: uuid.NewString(), Name: "managed-" + resource.String(), ModelVersionId: uuid.NewString(), Replicas: 1,
		Runtime:   &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, Provider: "kserve"},
		Engine:    &inferencev1.EngineSpec{Type: "vllm", Image: "engine:v1", Command: "engine serve /models"},
		Resource:  &inferencev1.ResourceSpec{Gpu: request, Requests: map[string]string{"cpu": "1", "memory": "8Gi"}},
		Context:   &inferencev1.CommandContext{TenantId: tenant.String(), ResourceId: resource.String(), OperationId: uuid.NewString()},
		ClusterId: cluster.String(), GpuExecution: execution,
		GpuDisplay: &acceleratorv1.GpuDisplay{ProfileId: profile.String(), ProfileVersion: 1, ProfileName: "test", ModelKey: "nvidia-test", Mode: acceleratorv1.SupplyMode_SHARED_FIXED},
	}
	refreshManagedFixtureDigests(t, req)
	return req
}

func refreshManagedFixtureDigests(t *testing.T, req *inferencev1.CreateInferenceServiceRequest) {
	t.Helper()
	digest, err := inferencev1.ManagedCreateRequestHash(req, req.Context.TenantId, "user", "42")
	if err != nil {
		t.Fatal(err)
	}
	req.Context.RequestDigest = digest
}

func managedDeleteFixture(t *testing.T, req *inferencev1.CreateInferenceServiceRequest) *inferencev1.DeleteInferenceServiceRequest {
	t.Helper()
	deleteID := uuid.NewString()
	digest, err := inferencev1.ManagedDeleteRequestHash(req.Context.TenantId, req.Context.ResourceId, req.Context.OperationId, "user", "42")
	if err != nil {
		t.Fatal(err)
	}
	return &inferencev1.DeleteInferenceServiceRequest{RequestId: uuid.NewString(), ResourceId: req.Context.ResourceId, CreateOperationId: req.Context.OperationId, Context: &inferencev1.CommandContext{TenantId: req.Context.TenantId, ResourceId: req.Context.ResourceId, OperationId: deleteID, RequestDigest: digest}}
}

func TestPostgresManagedGPUCreateAtomicReplayAndOriginalContext(t *testing.T) {
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	req := managedCreateFixture(t, tenant, resource)
	repo := NewRepository(pool)
	server := service.NewInferenceServerWithAll(NewCreateUseCase(repo), nil, NewCommandUseCase(repo), NewUpdateUseCase(repo))
	out, err := server.CreateInferenceService(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.GetResource().GetId() != resource.String() || out.GetOperation().GetId() != req.Context.OperationId || !out.GetDurableOwnerAck().GetAccepted() {
		t.Fatalf("Gov identities/ACK changed: %v", out)
	}
	reordered := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
	reordered.RequestId = uuid.NewString()
	replay, err := service.NewInferenceServer(NewCreateUseCase(NewRepository(pool))).CreateInferenceService(ctx, reordered)
	if err != nil || !proto.Equal(out, replay) {
		t.Fatalf("restart replay=%v error=%v", replay, err)
	}
	saved, err := repo.LoadRefundContext(ctx, tenant.String(), resource.String(), req.Context.OperationId)
	if err != nil || saved.Snapshot == nil || saved.Snapshot.ExecutionDigest != req.GpuExecution.ExecutionDigest || saved.DeleteOperationID != "" {
		t.Fatalf("lost original execution context: %+v error=%v", saved, err)
	}
	changed := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
	changed.Name = "different"
	refreshManagedFixtureDigests(t, changed)
	if _, err := server.CreateInferenceService(ctx, changed); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("same operation changed content accepted: %v", err)
	}
}

func TestPostgresManagedGPUConcurrentCommandHasOneACK(t *testing.T) {
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	req := managedCreateFixture(t, tenant, resource)
	server := service.NewInferenceServer(NewCreateUseCase(NewRepository(pool)))
	var wait sync.WaitGroup
	results := make(chan *inferencev1.OperationResponse, 8)
	errors := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			out, err := server.CreateInferenceService(ctx, proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest))
			results <- out
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original *inferencev1.OperationResponse
	for result := range results {
		if original == nil {
			original = result
		}
		if !proto.Equal(original, result) {
			t.Fatalf("concurrent ACK changed: %v versus %v", result, original)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_managed_gpu_commands WHERE tenant_id=$1`, tenant).Scan(&count); err != nil || count != 1 {
		t.Fatalf("command count=%d error=%v", count, err)
	}
}

func TestPostgresManagedGPUFrozenSnapshotProjectsSharedAndWholeReplicas(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "whole"}[whole], func(t *testing.T) {
			ctx, pool, tenant, resource := lifecycleDatabase(t)
			ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
			req := managedCreateFixture(t, tenant, resource)
			if whole {
				req.GpuDisplay.Mode = acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE
				req.GpuExecution.Resources["volcano.sh/vgpu-cores"] = "100"
				req.GpuExecution.Resources["volcano.sh/vgpu-memory"] = "0"
				req.GpuExecution.Resources["volcano.sh/vgpu-memory-percentage"] = "100"
			}
			req.Replicas, req.Resource.Gpu.Replicas = 2, 2
			refreshManagedFixtureDigests(t, req)
			if _, err := service.NewInferenceServer(NewCreateUseCase(NewRepository(pool))).CreateInferenceService(ctx, req); err != nil {
				t.Fatal(err)
			}
			loaded, err := NewRuntimeSource(pool, "models").CurrentRuntime(ctx, tenant.String(), resource.String(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.GPUSnapshot == nil || loaded.GPUSnapshot.ExecutionDigest != req.GpuExecution.ExecutionDigest || loaded.Resources.GPU.Replicas != 2 || loaded.Replicas != 2 {
				t.Fatalf("frozen runtime changed: %+v", loaded.RuntimeSpec)
			}
			obj, err := kubernetes.RenderKServeRuntime(loaded.RuntimeSpec)
			if err != nil {
				t.Fatal(err)
			}
			predictor, _, _ := unstructured.NestedMap(obj.Object, "spec", "predictor")
			if predictor["schedulerName"] != "volcano" || predictor["minReplicas"] != int64(2) || predictor["maxReplicas"] != int64(2) {
				t.Fatalf("frozen replica/scheduler projection lost: %v", predictor)
			}
		})
	}
}

func TestPostgresManagedGPUDeleteFirstClosesLateCreate(t *testing.T) {
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	req := managedCreateFixture(t, tenant, resource)
	repo := NewRepository(pool)
	server := service.NewInferenceServerWithCommands(NewCreateUseCase(repo), nil, NewCommandUseCase(repo))
	deletion := managedDeleteFixture(t, req)
	accepted, err := server.DeleteInferenceService(ctx, deletion)
	if err != nil || !accepted.GetDurableOwnerAck().GetAccepted() {
		t.Fatalf("DELETE first=%v error=%v", accepted, err)
	}
	replay, err := server.DeleteInferenceService(ctx, proto.Clone(deletion).(*inferencev1.DeleteInferenceServiceRequest))
	if err != nil || !proto.Equal(accepted, replay) {
		t.Fatalf("DELETE replay=%v error=%v", replay, err)
	}
	late, err := server.CreateInferenceService(ctx, req)
	if err != nil || !late.GetDurableOwnerAck().GetAccepted() || late.GetOperation().GetStep() != "blocked_by_delete" {
		t.Fatalf("late create=%v error=%v", late, err)
	}
	saved, err := repo.LoadRefundContext(ctx, tenant.String(), resource.String(), req.Context.OperationId)
	if err != nil || saved.DeleteOperationID != deletion.Context.OperationId {
		t.Fatalf("DELETE first lost immutable identity: %+v error=%v", saved, err)
	}
}

func TestPostgresManagedGPURejectsForgedIdentityDigestAndCharge(t *testing.T) {
	// Provider charge references are gone. The equivalent forgery checks now
	// cover the command identity, business digest and frozen execution.
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	req := managedCreateFixture(t, tenant, resource)
	server := service.NewInferenceServer(NewCreateUseCase(NewRepository(pool)))
	scoped := service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	tests := []struct {
		name   string
		ctx    context.Context
		mutate func(*inferencev1.CreateInferenceServiceRequest)
	}{
		{"untrusted caller", service.WithTenantID(ctx, tenant.String()), func(*inferencev1.CreateInferenceServiceRequest) {}},
		{"cross tenant", service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), uuid.NewString()), "user:42"), func(*inferencev1.CreateInferenceServiceRequest) {}},
		{"business digest", scoped, func(r *inferencev1.CreateInferenceServiceRequest) { r.Engine.Command = "tampered" }},
		{"request digest", scoped, func(r *inferencev1.CreateInferenceServiceRequest) { r.Context.RequestDigest = strings.Repeat("0", 64) }},
		{"missing execution", scoped, func(r *inferencev1.CreateInferenceServiceRequest) { r.GpuExecution = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bad := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
			test.mutate(bad)
			if _, err := server.CreateInferenceService(test.ctx, bad); err == nil {
				t.Fatal("forged command accepted")
			}
		})
	}
}

func TestManagedGPUContextJSONContainsOnlyExecutionSelection(t *testing.T) {
	snapshot := &gpu.Snapshot{Execution: &gpu.Execution{SchedulerName: "volcano", ExecutionDigest: strings.Repeat("a", 64)}, Display: &gpu.Display{ProfileID: uuid.NewString(), ProfileVersion: 1, ProfileName: "p", ModelKey: "m"}}
	encoded, err := json.Marshal(gpu.RefundContext{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "charge") || strings.Contains(string(encoded), "memory_encoding") {
		t.Fatalf("retired provider fields leaked into persisted context: %s", encoded)
	}
	var decoded gpu.RefundContext
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.Snapshot == nil || !reflect.DeepEqual(decoded.Snapshot.Execution, snapshot.Execution) {
		t.Fatalf("context roundtrip=%+v error=%v", decoded, err)
	}
}
