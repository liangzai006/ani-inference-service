package service

import (
	"context"
	"testing"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type fakeCreate struct{ in CreateInput }

type fakeUpdate struct{ in inferencebiz.UpdateInput }

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
func (fakeRead) ListServices(context.Context, string, int32, string) ([]*inferencev1.InferenceService, string, error) {
	return []*inferencev1.InferenceService{{Id: "svc-1"}}, "next", nil
}
func (fakeRead) ListOperations(context.Context, string, string, int32, string) ([]*inferencev1.Operation, string, error) {
	return []*inferencev1.Operation{{Id: "op-1"}}, "next", nil
}

func TestCreateRequiresTenant(t *testing.T) {
	_, err := NewInferenceServer(&fakeCreate{}).CreateInferenceService(context.Background(), &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", Replicas: 1})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestCreateNormalizesAndHashesDeterministically(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", RequestId: "r1", Replicas: 1,
		Resource: &inferencev1.ResourceSpec{Limits: map[string]string{"nvidia.com/gpu": "1", "memory": "8Gi"}}}
	ctx := WithTenantID(context.Background(), "tenant-a")
	if _, err := NewInferenceServer(f).CreateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.in.TenantID != "tenant-a" || f.in.Resources.Requests["nvidia.com/gpu"] != "1" {
		t.Fatalf("input not normalized: %+v", f.in)
	}
	if len(f.in.RequestHash) != 64 {
		t.Fatalf("hash=%q", f.in.RequestHash)
	}
	first := f.in.RequestHash
	// Map insertion order must not affect the deterministic request hash.
	req.Resource.Limits = map[string]string{"memory": "8Gi", "nvidia.com/gpu": "1"}
	if _, err := NewInferenceServer(f).CreateInferenceService(ctx, req); err != nil {
		t.Fatal(err)
	}
	if first != f.in.RequestHash {
		t.Fatalf("hash changed with map order: %s != %s", first, f.in.RequestHash)
	}
}

func TestCreatePropagatesAuditActor(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{Name: "svc", ModelVersionId: "mv", RequestId: "actor-1", Replicas: 1}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithActor(WithTenantID(context.Background(), "tenant-a"), "workload:caller"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.Actor != "workload:caller" {
		t.Fatalf("actor=%q", f.in.Actor)
	}
}

func TestCreatePassesExplicitEndpoint(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "endpoint-1", Name: "svc", ModelVersionId: "mv", Replicas: 1,
		Runtime: &inferencev1.RuntimeSpec{Endpoint: &inferencev1.EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: "8080", Protocol: "TCP"}}}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.Endpoint == nil || f.in.Endpoint.ContainerPort != 8080 || f.in.Endpoint.ServicePort != 80 {
		t.Fatalf("endpoint not passed to use case: %+v", f.in.Endpoint)
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
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "r1", Name: "svc", ModelVersionId: "mv", Replicas: 2}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatalf("scaled deployment was rejected: %v", err)
	}
}

func TestCreateRequiresUseCase(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{RequestId: "r1", Name: "svc", ModelVersionId: "mv", Replicas: 1}
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
	_, err = server.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{PageSize: 101})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestListDelegatesWithTenant(t *testing.T) {
	server := NewInferenceServerWithRead(nil, fakeRead{})
	out, err := server.ListInferenceServices(WithTenantID(context.Background(), "tenant-a"), &inferencev1.ListInferenceServicesRequest{PageSize: 1})
	if err != nil || len(out.GetServices()) != 1 || out.GetNextPageToken() != "next" {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestCreateAcceptsLeaderWorkerSetShape(t *testing.T) {
	f := &fakeCreate{}
	req := &inferencev1.CreateInferenceServiceRequest{
		RequestId: "r-lws", Name: "distributed", ModelVersionId: "mv", Replicas: 2,
		Runtime: &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 4},
	}
	if _, err := NewInferenceServer(f).CreateInferenceService(WithTenantID(context.Background(), "tenant-a"), req); err != nil {
		t.Fatal(err)
	}
	if f.in.RuntimeMode != "leader_worker_set" || f.in.WorkerReplicas != 4 || f.in.Replicas != 2 {
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
	_, err = s.UpdateInferenceService(WithTenantID(context.Background(), "tenant-a"), &inferencev1.UpdateInferenceServiceRequest{RequestId: "u2", ResourceId: "svc", ExpectedGeneration: 2, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"replicas"}}, Replicas: 1})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("partial mask code=%v err=%v", status.Code(err), err)
	}
}
