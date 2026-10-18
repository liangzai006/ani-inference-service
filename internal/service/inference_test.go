package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	gpubiz "github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type fakeCreate struct{ in CreateInput }

type fakeUpdate struct{ in inferencebiz.UpdateInput }

func validEngine() *inferencev1.EngineSpec {
	return &inferencev1.EngineSpec{Type: "vllm", Image: "engine:v1", Command: "vllm serve /models"}
}

func TestRequireTenantRejectsAbsentTrustedContext(t *testing.T) {
	// Zero-trust: absence of trusted context always fails, even with directTenant.
	_, err := RequireTenant(context.Background(), "tenant-a")
	if !errors.Is(err, ErrMissingTenant) {
		t.Fatalf("error = %v; want ErrMissingTenant", err)
	}
}

func TestRequireTenantRejectsDirectTenantMismatch(t *testing.T) {
	_, err := RequireTenant(WithTenantID(context.Background(), "tenant-a"), "tenant-b")
	if !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("error = %v, want tenant mismatch", err)
	}
}

func TestRequireTenantAcceptsMatchingDirectTenant(t *testing.T) {
	// With trusted context, matching directTenant is accepted for defensive verification.
	got, err := RequireTenant(WithTenantID(context.Background(), "tenant-a"), "tenant-a")
	if err != nil || got != "tenant-a" {
		t.Fatalf("tenant = %q, err = %v; want tenant-a", got, err)
	}
}

func TestRequireTenantAcceptsTrustedWithoutDirect(t *testing.T) {
	// Trusted context alone is sufficient; directTenant is optional.
	got, err := RequireTenant(WithTenantID(context.Background(), "tenant-a"), "")
	if err != nil || got != "tenant-a" {
		t.Fatalf("tenant = %q, err = %v; want tenant-a", got, err)
	}
}

func (f *fakeUpdate) Update(_ context.Context, in inferencebiz.UpdateInput) (*inferencev1.OperationResponse, error) {
	f.in = in
	return &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: "op-update", Phase: "pending"}}, nil
}

func (f *fakeCreate) Create(_ context.Context, in CreateInput) (*inferencev1.OperationResponse, error) {
	f.in = in
	return &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: "op-1", Phase: "pending"}}, nil
}

type fakeRead struct{}

func (fakeRead) GetService(context.Context, string, string) (*inferencev1.InferenceService, error) {
	return &inferencev1.InferenceService{Id: "svc-1"}, nil
}
func (fakeRead) GetOperation(context.Context, string, string) (*inferencev1.Operation, error) {
	return &inferencev1.Operation{Id: "op-1"}, nil
}
func (fakeRead) ListServicesPage(context.Context, string, inferencebiz.ListOptions) ([]*inferencev1.InferenceService, int64, error) {
	return []*inferencev1.InferenceService{{Id: "svc-1"}}, 1, nil
}
func (fakeRead) ListOperationsPage(context.Context, string, string, inferencebiz.ListOptions) ([]*inferencev1.Operation, int64, error) {
	return []*inferencev1.Operation{{Id: "op-1"}}, 1, nil
}

func TestCreateRequiresTenant(t *testing.T) {
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(context.Background(), &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", Replicas: 1})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestCreateRequiresCallerEngineConfiguration(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "engine-required", Name: "svc", ModelVersionId: "mv", Replicas: 1}
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestCreateRejectsKServeInvalidNameBeforeUseCase(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: "invalid-name", Name: "qwen2-0.5b-infer-e2e", ModelVersionId: "mv", Replicas: 1,
		Engine: validEngine(),
	}
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestCreateNormalizesAndHashesDeterministically(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", RequestId: "r1", Replicas: 1, Engine: validEngine(),
		Resource: &inferencev1.ResourceSpec{Limits: map[string]string{"cpu": "2", "memory": "8Gi"}}}
	ctx := WithTenantID(context.Background(), "tenant-a")
	if _, err := NewInferenceServer(f).CreateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.in.TenantID != "tenant-a" || f.in.Resources.Limits["memory"] != "8Gi" {
		t.Fatalf("input not normalized: %+v", f.in)
	}
	if len(f.in.RequestHash) != 64 {
		t.Fatalf("hash=%q", f.in.RequestHash)
	}
	first := f.in.RequestHash
	// Map insertion order must not affect the deterministic request hash.
	req.Resource.Limits = map[string]string{"memory": "8Gi", "cpu": "2"}
	if _, err := NewInferenceServer(f).CreateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if first != f.in.RequestHash {
		t.Fatalf("hash changed with map order: %s != %s", first, f.in.RequestHash)
	}
}

func TestCreateRejectsLegacyGPUMapWithoutNestedRequest(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: "legacy-gpu-map", Name: "gpu", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(),
		Resource: &inferencev1.ResourceSpec{Limits: map[string]string{"nvidia.com/gpu": "1"}},
	}
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestCreateRejectsNestedGpuRequestWithoutGovernanceAttachment(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "gpu-1", Name: "gpu", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(), Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{
		ClusterId: "10000000-0000-4000-8000-000000000001", PoolId: "10000000-0000-4000-8000-000000000002", ProfileId: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container",
	}}}
	_, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.PermissionDenied || f.in.Resources.GPU != nil {
		t.Fatalf("GPU without Governance command context accepted: input=%+v error=%v", f.in, err)
	}
}

func TestManagedCreatePersistsACCExecutionDigest(t *testing.T) {
	tenant, resource, operation := uuid.New(), uuid.New(), uuid.New()
	cluster, pool, profile := uuid.New(), uuid.New(), uuid.New()
	execution := &acceleratorv1.GpuExecution{
		SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia",
		NodeSelector: map[string]string{
			"accelerator.ani.io/supply-group": pool.String(),
			"accelerator.ani.io/model-key":    "nvidia-test",
			"accelerator.ani.io/baseline-id":  profile.String(),
		},
		PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"},
		Resources:      map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "6144", "volcano.sh/vgpu-cores": "25"},
	}
	var err error
	execution.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(execution)
	if err != nil {
		t.Fatal(err)
	}
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: uuid.NewString(), Name: "managed-digest", ModelVersionId: uuid.NewString(), Replicas: 1,
		Runtime:   &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, Provider: "kserve"},
		Engine:    validEngine(),
		Resource:  &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{ClusterId: cluster.String(), PoolId: pool.String(), ProfileId: profile.String(), ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}},
		Context:   &inferencev1.CommandContext{TenantId: tenant.String(), ResourceId: resource.String(), OperationId: operation.String()},
		ClusterId: cluster.String(), GpuExecution: execution,
		GpuDisplay: &acceleratorv1.GpuDisplay{ProfileId: profile.String(), ProfileVersion: 1, ProfileName: "test", ModelKey: "nvidia-test", Mode: acceleratorv1.SupplyMode_SHARED_FIXED},
	}
	req.Context.RequestDigest, err = inferencev1.ManagedCreateRequestHash(req, tenant.String(), "user", "42")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCreate{}
	ctx := WithActor(WithTenantID(gpubiz.WithGovernanceIdentity(context.Background()), tenant.String()), "user:42")
	if _, err := NewInferenceServer(f).CreateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.in.GPUSnapshotDigest != execution.ExecutionDigest {
		t.Fatalf("persisted GPU plan digest=%q, want ACC execution digest %q", f.in.GPUSnapshotDigest, execution.ExecutionDigest)
	}
}

func TestCreatePropagatesAuditActor(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", RequestId: "actor-1", Replicas: 1, Engine: validEngine()}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithActor(WithTenantID(context.Background(), "tenant-a"), "workload:caller"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.Actor != "workload:caller" {
		t.Fatalf("actor=%q", f.in.Actor)
	}
}

func TestCreatePassesExplicitEndpoint(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "endpoint-1", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Endpoint: &inferencev1.EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: "8080", Protocol: "TCP"}}}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.Endpoint == nil || f.in.Endpoint.ContainerPort != 8080 || f.in.Endpoint.ServicePort != 80 {
		t.Fatalf("endpoint not passed to use case: %+v", f.in.Endpoint)
	}
}

func TestCreatePassesRuntimeProvider(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "provider-1", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Provider: "kserve"}}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeProvider != "kserve" {
		t.Fatalf("runtime provider=%q, want kserve", f.in.RuntimeProvider)
	}
}

func TestCreateDefaultsRuntimeProviderToKServe(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "provider-default", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine()}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeProvider != "kserve" {
		t.Fatalf("runtime provider=%q, want kserve", f.in.RuntimeProvider)
	}
}

func TestCreateRejectsUnsupportedRuntimeProvider(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "provider-invalid", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Provider: "unknown"}}
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestCreateRejectsLegacyDeploymentProvider(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "provider-legacy", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Provider: "deployment"}}
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestUpdatePropagatesAuditActor(t *testing.T) {
	f := &fakeUpdate{}
	server := NewInferenceServerWithAll(nil, nil, nil, f)
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "actor-update", ResourceId: "svc", ExpectedGeneration: 1,
		Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "1"}}, Replicas: 1,
		Runtime:    &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}}
	if _, err := server.UpdateInferenceService(WithActor(WithTenantID(context.Background(), "tenant-a"), "workload:caller"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.Actor != "workload:caller" {
		t.Fatalf("actor=%q", f.in.Actor)
	}
}

func TestUpdatePassesModelVersionID(t *testing.T) {
	f := &fakeUpdate{}
	server := NewInferenceServerWithAll(nil, nil, nil, f)
	req := &inferencev1.UpdateInferenceServiceRequest{
		RequestId: "switch-version", ResourceId: "svc", ExpectedGeneration: 1,
		ModelVersionId: "version-new",
		Resource:       &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "1"}}, Replicas: 1,
		Runtime:    &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime", "model_version_id"}},
	}
	if _, err := server.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.ModelVersionID != "version-new" {
		t.Fatalf("model version id=%q", f.in.ModelVersionID)
	}
}

func TestUpdateRequiresModelVersionMaskConsistency(t *testing.T) {
	server := NewInferenceServerWithAll(nil, nil, nil, &fakeUpdate{})
	req := &inferencev1.UpdateInferenceServiceRequest{
		RequestId: "switch-version-missing-mask", ResourceId: "svc", ExpectedGeneration: 1,
		ModelVersionId: "version-new", Replicas: 1,
		Runtime:    &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}},
	}
	_, err := server.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestCreateAcceptsScaledDeployment(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "r1", Name: "svc", ModelVersionId: "mv", Replicas: 2, Engine: validEngine()}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatalf("scaled deployment was rejected: %v", err)
	}
}

func TestCreateRequiresUseCase(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "r1", Name: "svc", ModelVersionId: "mv", Replicas: 1, Engine: validEngine()}
	_, err := NewInferenceServer().CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestReadRequiresTenant(t *testing.T) {
	server := NewInferenceServerWithRead(nil, fakeRead{})
	_, err := server.GetOperation(context.Background(), &inferencev1.GetOperationRequest{OperationId: "op-1"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestReadDelegatesWithTenant(t *testing.T) {
	server := NewInferenceServerWithRead(nil, fakeRead{})
	out, err := server.GetInferenceService(WithTenantID(context.Background(), "tenant-a"), &inferencev1.GetInferenceServiceRequest{ResourceId: "svc-1"})
	if err != nil || out.GetId() != "svc-1" {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestListRequiresBoundedPageSizeAndTenant(t *testing.T) {
	server := NewInferenceServerWithRead(nil, fakeRead{})
	_, err := server.ListInferenceServices(context.Background(), &inferencev1.ListInferenceServicesRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
	_, err = server.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), func() *inferencev1.ListInferenceServicesRequest {
		v := int32(101)
		return &inferencev1.ListInferenceServicesRequest{PageSize: &v}
	}())
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestListDelegatesWithTenant(t *testing.T) {
	server := NewInferenceServerWithRead(nil, fakeRead{})
	page, size := int32(1), int32(1)
	out, err := server.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Page: &page, PageSize: &size})
	if err != nil || len(out.GetItems()) != 1 || out.GetTotal() != 1 {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

type paginationRead struct {
	services         []*inferencev1.InferenceService
	operations       []*inferencev1.Operation
	serviceOptions   []inferencebiz.ListOptions
	operationOptions []inferencebiz.ListOptions
}

func TestListServicesNormalizesSupportedFilters(t *testing.T) {
	r := &paginationRead{}
	s := NewInferenceServerWithRead(nil, r)
	statusFilter, capability := " RUNNING ", " Vision "
	_, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Status: statusFilter, Capability: capability})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.serviceOptions) != 1 || r.serviceOptions[0].Status != "running" || r.serviceOptions[0].Capability != "vision" {
		t.Fatalf("options=%+v", r.serviceOptions)
	}
}

func TestListServicesRejectsUnsupportedStatus(t *testing.T) {
	r := &paginationRead{}
	s := NewInferenceServerWithRead(nil, r)
	_, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Status: "paused"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
	if len(r.serviceOptions) != 0 {
		t.Fatalf("read called for invalid status: %+v", r.serviceOptions)
	}
}

func (r *paginationRead) GetService(context.Context, string, string) (*inferencev1.InferenceService, error) {
	return nil, errors.New("unused")
}
func (r *paginationRead) GetOperation(context.Context, string, string) (*inferencev1.Operation, error) {
	return nil, errors.New("unused")
}
func (r *paginationRead) ListServicesPage(_ context.Context, tenant string, options inferencebiz.ListOptions) ([]*inferencev1.InferenceService, int64, error) {
	r.serviceOptions = append(r.serviceOptions, options)
	start := int(options.Offset)
	if start >= len(r.services) {
		return []*inferencev1.InferenceService{}, int64(len(r.services)), nil
	}
	end := start + int(options.Limit)
	if end > len(r.services) {
		end = len(r.services)
	}
	return r.services[start:end], int64(len(r.services)), nil
}
func (r *paginationRead) ListOperationsPage(_ context.Context, tenant, resource string, options inferencebiz.ListOptions) ([]*inferencev1.Operation, int64, error) {
	r.operationOptions = append(r.operationOptions, options)
	start := int(options.Offset)
	if start >= len(r.operations) {
		return []*inferencev1.Operation{}, int64(len(r.operations)), nil
	}
	end := start + int(options.Limit)
	if end > len(r.operations) {
		end = len(r.operations)
	}
	return r.operations[start:end], int64(len(r.operations)), nil
}

func TestListServicesUsesPageOffsetAndStableTotal(t *testing.T) {
	r := &paginationRead{}
	for i := 0; i < 45; i++ {
		r.services = append(r.services, &inferencev1.InferenceService{Id: fmt.Sprintf("svc-%02d", i)})
	}
	s := NewInferenceServerWithRead(nil, r)
	page, size := int32(3), int32(2)
	out, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Page: &page, PageSize: &size})
	if err != nil || len(out.GetItems()) != 2 || out.GetTotal() != 45 || out.GetItems()[0].GetId() != "svc-04" {
		t.Fatalf("page=%v err=%v", out, err)
	}
	if got := r.serviceOptions[0]; got.Offset != 4 || got.Limit != 2 {
		t.Fatalf("options=%+v", got)
	}
}

func TestListDefaultsAndEmptyPage(t *testing.T) {
	r := &paginationRead{}
	for i := 0; i < 21; i++ {
		r.services = append(r.services, &inferencev1.InferenceService{Id: fmt.Sprintf("svc-%02d", i)})
	}
	s := NewInferenceServerWithRead(nil, r)
	out, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{})
	if err != nil || len(out.GetItems()) != 20 || out.GetTotal() != 21 {
		t.Fatalf("default=%v err=%v", out, err)
	}
	page, size := int32(100), int32(100)
	empty, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Page: &page, PageSize: &size})
	if err != nil || len(empty.GetItems()) != 0 || empty.GetTotal() != 21 {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
}

func TestListRejectsInvalidPageValuesBeforeRead(t *testing.T) {
	r := &paginationRead{}
	s := NewInferenceServerWithRead(nil, r)
	for _, tc := range []struct {
		name       string
		page, size int32
	}{
		{"page zero", 0, 20}, {"page too large", 101, 20}, {"size zero", 1, 0}, {"size too large", 1, 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{Page: &tc.page, PageSize: &tc.size})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if len(r.serviceOptions) != 0 {
		t.Fatalf("read called for invalid page: %+v", r.serviceOptions)
	}
}

func TestListOperationsPreservesResourceFilterAndTotal(t *testing.T) {
	r := &paginationRead{operations: []*inferencev1.Operation{{Id: "op-1", ServiceId: "svc-a"}, {Id: "op-2", ServiceId: "svc-a"}, {Id: "op-3", ServiceId: "svc-a"}}}
	s := NewInferenceServerWithRead(nil, r)
	page, size := int32(2), int32(2)
	out, err := s.ListOperations(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListOperationsRequest{ResourceId: "svc-a", Page: &page, PageSize: &size})
	if err != nil || len(out.GetItems()) != 1 || out.GetTotal() != 3 {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if got := r.operationOptions[0]; got.Offset != 2 || got.Limit != 2 {
		t.Fatalf("options=%+v", got)
	}
}

func TestCreateAcceptsLeaderWorkerSetShape(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: "r-lws", Name: "distributed", ModelVersionId: "mv", Replicas: 2,
		Engine:  validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 4},
	}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeMode != "leader_worker_set" || f.in.WorkerReplicas != 4 || f.in.Replicas != 2 {
		t.Fatalf("runtime shape not preserved: %+v", f.in)
	}
}

func TestCreateAcceptsKServeLeaderWorkerSetShape(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: "r-kserve-lws", Name: "distributed-kserve", ModelVersionId: "mv", Replicas: 1,
		Engine:  validEngine(),
		Runtime: &inferencev1.RuntimeSpec{Provider: "kserve", Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 1},
	}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeProvider != "kserve" || f.in.RuntimeMode != "leader_worker_set" {
		t.Fatalf("runtime shape not preserved: %+v", f.in)
	}
}

func TestUpdateRequiresFullSupportedMaskAndPreservesRuntime(t *testing.T) {
	f := &fakeUpdate{}
	s := NewInferenceServerWithAll(nil, nil, nil, f)
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "u1", ResourceId: "svc", ExpectedGeneration: 2,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}, Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "2"}}, Replicas: 1,
		Runtime: &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 2}}
	out, err := s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if err != nil || out.GetOperation().GetId() != "op-update" || f.in.RuntimeMode != "leader_worker_set" || f.in.WorkerReplicas != 2 {
		t.Fatalf("out=%v err=%v input=%+v", out, err, f.in)
	}
	if f.in.RuntimeProvider != "" {
		t.Fatalf("omitted runtime provider=%q, want inherit marker", f.in.RuntimeProvider)
	}
	_, err = s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), &inferencev1.UpdateInferenceServiceRequest{RequestId: "u2", ResourceId: "svc", ExpectedGeneration: 2, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"replicas"}}, Replicas: 1})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("partial mask code=%v err=%v", status.Code(err), err)
	}
}

func TestUpdatePassesRuntimeProvider(t *testing.T) {
	f := &fakeUpdate{}
	s := NewInferenceServerWithAll(nil, nil, nil, f)
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "provider-update", ResourceId: "svc", ExpectedGeneration: 2,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}, Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "2"}}, Replicas: 1,
		Runtime: &inferencev1.RuntimeSpec{Provider: "kserve"}}
	if _, err := s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeProvider != "kserve" {
		t.Fatalf("runtime provider=%q, want kserve", f.in.RuntimeProvider)
	}
}

func TestUpdateRejectsLegacyGPUMapWithoutNestedRequest(t *testing.T) {
	s := NewInferenceServerWithAll(nil, nil, nil, &fakeUpdate{})
	req := &inferencev1.UpdateInferenceServiceRequest{
		RequestId: "legacy-gpu-update", ResourceId: "svc", ExpectedGeneration: 1,
		Resource: &inferencev1.ResourceSpec{Limits: map[string]string{"nvidia.com/gpu": "1"}}, Replicas: 1,
		Runtime:    &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}},
	}
	_, err := s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestUpdateRejectsLegacyDeploymentProvider(t *testing.T) {
	s := NewInferenceServerWithAll(nil, nil, nil, &fakeUpdate{})
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "provider-update-legacy", ResourceId: "svc", ExpectedGeneration: 2,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}, Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "2"}}, Replicas: 1,
		Runtime: &inferencev1.RuntimeSpec{Provider: "deployment"}}
	_, err := s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}

func TestUpdateRejectsDirectGPUMutation(t *testing.T) {
	s := NewInferenceServerWithAll(nil, nil, nil, &fakeUpdate{})
	const id = "10000000-0000-4000-8000-000000000001"
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "gpu-update", ResourceId: "svc", ExpectedGeneration: 1,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}, Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{ClusterId: id, PoolId: id, ProfileId: id, ProfileVersion: 2, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}}, Replicas: 1,
		Runtime: &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT}}
	if _, err := s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%v err=%v, want PermissionDenied", status.Code(err), err)
	}
}

func TestManagedUpdateValidatesFrozenExecutionAndDigest(t *testing.T) {
	tenant, resource, operation := uuid.New(), uuid.New(), uuid.New()
	cluster, pool, profile := uuid.New(), uuid.New(), uuid.New()
	execution := &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia", NodeSelector: map[string]string{
		"accelerator.ani.io/supply-group": pool.String(), "accelerator.ani.io/model-key": "nvidia-test", "accelerator.ani.io/baseline-id": profile.String(),
	}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "6144", "volcano.sh/vgpu-cores": "25"}}
	var err error
	execution.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(execution)
	if err != nil {
		t.Fatal(err)
	}
	req := &inferencev1.UpdateInferenceServiceRequest{RequestId: "managed-update", ResourceId: resource.String(), ExpectedGeneration: 1,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"resource", "replicas", "runtime"}}, Replicas: 1,
		Runtime:  &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, Provider: "kserve"},
		Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{ClusterId: cluster.String(), PoolId: pool.String(), ProfileId: profile.String(), ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}},
		Context:  &inferencev1.CommandContext{TenantId: tenant.String(), ResourceId: resource.String(), OperationId: operation.String()}, ClusterId: cluster.String(), GpuExecution: execution,
		GpuDisplay: &acceleratorv1.GpuDisplay{ProfileId: profile.String(), ProfileVersion: 1, ProfileName: "test", ModelKey: "nvidia-test", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}}
	req.Context.RequestDigest, err = inferencev1.ManagedUpdateRequestHash(req, tenant.String(), "user", "42")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeUpdate{}
	ctx := WithActor(WithTenantID(gpubiz.WithGovernanceIdentity(context.Background()), tenant.String()), "user:42")
	if _, err := NewInferenceServerWithAll(nil, nil, nil, f).UpdateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.in.GPUSnapshot == nil || f.in.GPUSnapshotDigest != execution.ExecutionDigest || f.in.ManagedGPUOperationID != operation.String() {
		t.Fatalf("managed update lost frozen execution/operation: snapshot=%+v digest=%q operation=%q", f.in.GPUSnapshot, f.in.GPUSnapshotDigest, f.in.ManagedGPUOperationID)
	}
	// Re-signing the command cannot make an execution digest mismatch valid.
	req.GpuExecution.QueueName = "tampered"
	req.Context.RequestDigest, err = inferencev1.ManagedUpdateRequestHash(req, tenant.String(), "user", "42")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewInferenceServerWithAll(nil, nil, nil, &fakeUpdate{}).UpdateInferenceService(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed managed execution code=%v err=%v, want InvalidArgument", status.Code(err), err)
	}
}
