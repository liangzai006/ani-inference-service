package service

import (
	"context"
	"fmt"
	"strings"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func managedSnapshot(req *inferencev1.CreateInferenceServiceRequest) (*gpu.Snapshot, error) {
	if req == nil || req.GetResource() == nil || req.GetResource().GetGpu() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource.gpu is required")
	}
	execution, display := req.GetGpuExecution(), req.GetGpuDisplay()
	if execution == nil || display == nil || req.GetClusterId() == "" {
		return nil, status.Error(codes.InvalidArgument, "frozen GPU execution, display and cluster_id are required")
	}
	request := gpuInput(req.GetResource().GetGpu())
	snapshot := &gpu.Snapshot{Request: request, Execution: &gpu.Execution{SchedulerName: execution.GetSchedulerName(), QueueName: execution.GetQueueName(), RuntimeClassName: execution.GetRuntimeClassName(), NodeSelector: cloneMap(execution.GetNodeSelector()), PodAnnotations: cloneMap(execution.GetPodAnnotations()), Resources: cloneMap(execution.GetResources()), ExecutionDigest: execution.GetExecutionDigest()}, Display: &gpu.Display{ProfileID: display.GetProfileId(), ProfileVersion: display.GetProfileVersion(), ProfileName: display.GetProfileName(), ModelKey: display.GetModelKey(), Mode: int32(display.GetMode())}, ExecutionDigest: execution.GetExecutionDigest()}
	if request.ClusterID != req.GetClusterId() {
		return nil, status.Error(codes.InvalidArgument, "GPU cluster_id differs from frozen execution")
	}
	if err := gpu.ValidateSnapshot(snapshot, request); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return snapshot, nil
}

func (s *InferenceServer) managedCreate(ctx context.Context, req *inferencev1.CreateInferenceServiceRequest) (*inferencebiz.ManagedCommand, error) {
	if !gpu.GovernanceIdentity(ctx) {
		return nil, status.Error(codes.PermissionDenied, "GPU commands require the authenticated Governance service")
	}
	context := req.GetContext()
	if err := inferencev1.ValidateCommandContext(context); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	tenant, ok := TenantID(ctx)
	if !ok || tenant != context.GetTenantId() {
		return nil, status.Error(codes.PermissionDenied, "GPU command tenant does not match authenticated scope")
	}
	if err := inferencev1.ValidateManagedCreate(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	class := "cpu"
	var snapshot *gpu.Snapshot
	var err error
	if req.GetResource().GetGpu() != nil {
		class = "gpu"
		if !s.managedGPUConfigured() {
			return nil, status.Error(codes.FailedPrecondition, "managed GPU dependencies are not ready")
		}
		if err := s.validateManagedGPUClusterID(req.GetClusterId()); err != nil {
			return nil, err
		}
		snapshot, err = managedSnapshot(req)
		if err != nil {
			return nil, err
		}
	}
	if err := inferencev1.ValidateBusinessPayload(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	business, err := inferencev1.CanonicalBusinessPayload(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	digest, err := inferencev1.BusinessPayloadDigest(req)
	if err != nil {
		return nil, err
	}
	actor := Actor(ctx)
	requestHash, err := inferencev1.ManagedCreateCommandHash(req, tenant, actorType(actor), actorID(actor))
	if err != nil {
		return nil, err
	}
	if requestHash != context.GetRequestDigest() {
		return nil, status.Error(codes.InvalidArgument, "Governance CREATE request digest mismatch")
	}
	canonical := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
	canonical.RequestId = ""
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(canonical)
	if err != nil {
		return nil, err
	}
	frozen := gpu.RefundContext{TenantID: tenant, ResourceID: context.GetResourceId(), OriginalCreateOperationID: context.GetOperationId(), OwnerService: "ani-inference", ResourceClass: class, CreateCommandDigest: context.GetRequestDigest(), ClusterID: req.GetClusterId(), Snapshot: snapshot}
	return &inferencebiz.ManagedCommand{Context: frozen, ResourceClass: class, CreateCommandDigest: context.GetRequestDigest(), Actor: actor, Payload: payload, Business: business, BusinessDigest: digest, RequestHash: context.GetRequestDigest()}, nil
}

// managedUpdateSnapshot validates an optional frozen GPU attachment on an
// update. Business-only managed updates omit these fields and let the durable
// store reuse the original accepted snapshot; when present, every frozen field
// must be supplied and must pass the same ACC contract as CREATE.
func managedUpdateSnapshot(req *inferencev1.UpdateInferenceServiceRequest) (*gpu.Snapshot, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "update request is required")
	}
	if err := inferencev1.ValidateFrozenGPU(req.GetClusterId(), req.GetGpuExecution(), req.GetGpuDisplay(), req.GetResource().GetGpu()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	create := &inferencev1.CreateInferenceServiceRequest{
		Resource:       req.GetResource(),
		Replicas:       req.GetReplicas(),
		Runtime:        req.GetRuntime(),
		ModelVersionId: req.GetModelVersionId(),
		Engine:         req.GetEngine(),
		ClusterId:      req.GetClusterId(),
		GpuExecution:   req.GetGpuExecution(),
		GpuDisplay:     req.GetGpuDisplay(),
	}
	return managedSnapshot(create)
}

func (s *InferenceServer) DeleteInferenceService(ctx context.Context, req *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	if req.GetContext() == nil {
		if req.GetManagedDeleteScope() != nil {
			return nil, status.Error(codes.InvalidArgument, "managed_delete_scope requires trusted command context")
		}
		return s.acceptCommand(ctx, "delete", &inferencev1.ServiceCommandRequest{RequestId: req.GetRequestId(), ResourceId: req.GetResourceId(), ExpectedGeneration: req.GetExpectedGeneration()})
	}
	if err := inferencev1.ValidateManagedDelete(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.GetManagedDeleteScope().GetResourceClass() == inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU && !s.managedGPUConfigured() {
		return nil, status.Error(codes.FailedPrecondition, "managed GPU dependencies are not ready")
	}
	if err := inferencev1.ValidateCommandContext(req.GetContext()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if !gpu.GovernanceIdentity(ctx) {
		return nil, status.Error(codes.PermissionDenied, "GPU commands require the authenticated Governance service")
	}
	tenant, ok := TenantID(ctx)
	if !ok || tenant != req.GetContext().GetTenantId() {
		return nil, status.Error(codes.PermissionDenied, "GPU command tenant does not match authenticated scope")
	}
	if req.GetResourceId() != req.GetContext().GetResourceId() || req.GetCreateOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid managed DELETE identity")
	}
	actor := Actor(ctx)
	expected, err := inferencev1.ManagedDeleteCommandHash(req, tenant, actorType(actor), actorID(actor))
	if err != nil {
		return nil, err
	}
	if expected != req.GetContext().GetRequestDigest() {
		return nil, status.Error(codes.InvalidArgument, "Governance DELETE request digest mismatch")
	}
	canonical := proto.Clone(req).(*inferencev1.DeleteInferenceServiceRequest)
	canonical.RequestId = ""
	canonical.ExpectedGeneration = 0
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(canonical)
	if err != nil {
		return nil, err
	}
	// DELETE carries only the stable command identity. The persisted CREATE
	// context remains the source of the frozen cluster/snapshot used by release
	// observation; copying tenant_id into cluster_id would forge that context.
	class := "cpu"
	if req.GetManagedDeleteScope().GetResourceClass() == inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU {
		class = "gpu"
		if err := s.validateManagedGPUClusterID(req.GetManagedDeleteScope().GetClusterId()); err != nil {
			return nil, err
		}
	}
	frozen := gpu.RefundContext{ResourceClass: class, CreateCommandDigest: req.GetManagedDeleteScope().GetCreateCommandDigest(), ClusterID: req.GetManagedDeleteScope().GetClusterId(), TenantID: tenant, ResourceID: req.GetResourceId(), OriginalCreateOperationID: req.GetCreateOperationId(), DeleteOperationID: req.GetContext().GetOperationId(), OwnerService: "ani-inference"}
	if s.command == nil {
		return nil, status.Error(codes.FailedPrecondition, "inference command use case is not configured")
	}
	out, err := s.command.Command(ctx, inferencebiz.CommandInput{TenantID: tenant, RequestID: req.GetRequestId(), Actor: actor, ServiceID: req.GetResourceId(), Kind: "delete", RequestHash: hashBytes(payload), Managed: &inferencebiz.ManagedCommand{Context: frozen, ResourceClass: class, CreateCommandDigest: frozen.CreateCommandDigest, Actor: actor, Payload: payload, RequestHash: req.GetContext().GetRequestDigest()}})
	if err != nil {
		return nil, commandStatusError(err)
	}
	return out, nil
}

func actorType(actor string) string { v, _, _ := splitActor(actor); return v }
func actorID(actor string) string   { _, v, _ := splitActor(actor); return v }
func splitActor(actor string) (string, string, error) {
	// The authenticated transport envelope identifies Governance; the command
	// digest binds the delegated subject inside it, independently of that owner.
	enveloped := strings.HasPrefix(actor, "governance:")
	actor = strings.TrimPrefix(actor, "governance:")
	for i := 0; i < len(actor); i++ {
		if actor[i] == ':' {
			typeName := actor[:i]
			if enveloped && typeName == "access-key" {
				typeName = "api_key"
			}
			return typeName, actor[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("actor must be type:id")
}
func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
