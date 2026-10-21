package contractv1

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	ManagedCreateSchema = "gov-inference-create-v2"
	ManagedDeleteSchema = "gov-inference-delete-v2"
	ManagedUpdateSchema = "gov-inference-update-v2"
)

// ExecutionTopology is the Inference-owned mapping from business topology to
// execution containers. devicesPerUnit=0 selects CPU and never requests ACC.
type ExecutionTopology struct {
	ExecutionReplicas uint32
	ContainerName     string
	DevicesPerReplica uint32
	TotalDevices      int64
}

func NormalizeTopology(runtime *inferencev1.RuntimeSpec, replicas int32, devicesPerUnit uint32) (*ExecutionTopology, error) {
	if replicas < 1 {
		return nil, fmt.Errorf("replicas must be positive")
	}
	provider := strings.ToLower(strings.TrimSpace(runtime.GetProvider()))
	if provider != "" && provider != "kserve" {
		return nil, fmt.Errorf("runtime provider must be kserve")
	}
	pods, container := int64(replicas), "kserve-container"
	switch runtime.GetMode() {
	case inferencev1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED, inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT:
		if runtime.GetWorkerReplicas() < 0 || runtime.GetWorkerReplicas() > 1 {
			return nil, fmt.Errorf("deployment cannot specify multiple workers")
		}
	case inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET:
		if runtime.GetWorkerReplicas() < 1 {
			return nil, fmt.Errorf("LWS worker_replicas must be positive")
		}
		if devicesPerUnit != 0 {
			return nil, fmt.Errorf("managed GPU LWS requires a synchronous pre-create projection hook")
		}
		pods *= int64(runtime.GetWorkerReplicas()) + 1
		container = "main"
	default:
		return nil, fmt.Errorf("unsupported runtime mode")
	}
	if pods > math.MaxInt32 {
		return nil, fmt.Errorf("runtime topology overflows execution container count")
	}
	if devicesPerUnit > 0 && pods > 16 {
		return nil, fmt.Errorf("GPU execution container count exceeds 16")
	}
	if devicesPerUnit > 0 && pods > math.MaxInt64/int64(devicesPerUnit) {
		return nil, fmt.Errorf("GPU topology overflows total devices")
	}
	return &ExecutionTopology{ExecutionReplicas: uint32(pods), ContainerName: container, DevicesPerReplica: devicesPerUnit, TotalDevices: pods * int64(devicesPerUnit)}, nil
}

// ManagedResourceClassForCreate rejects mixed CPU/GPU attachments. It does not
// infer classification from charges; those remain exclusively in Governance.
func ManagedResourceClassForCreate(req *inferencev1.CreateInferenceServiceRequest) (inferencev1.ManagedResourceClass, error) {
	if req == nil || req.GetResource() == nil {
		return inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_UNSPECIFIED, fmt.Errorf("resource is required")
	}
	if req.GetResource().GetGpu() == nil {
		if req.GetClusterId() != "" || req.GetGpuExecution() != nil || req.GetGpuDisplay() != nil {
			return inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_UNSPECIFIED, fmt.Errorf("CPU commands cannot carry GPU attachments")
		}
		return inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU, nil
	}
	if req.GetClusterId() == "" || req.GetGpuExecution() == nil || req.GetGpuDisplay() == nil {
		return inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_UNSPECIFIED, fmt.Errorf("GPU commands require frozen cluster, execution and display")
	}
	return inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU, nil
}

func ValidateManagedCreate(req *inferencev1.CreateInferenceServiceRequest) error {
	if req == nil {
		return fmt.Errorf("create request is required")
	}
	if err := ValidateCommandContext(req.GetContext()); err != nil {
		return err
	}
	if req.GetModelArtifact() != nil {
		return fmt.Errorf("managed CREATE artifact must be resolved by Model")
	}
	if err := ValidateBusinessPayload(req); err != nil {
		return err
	}
	if _, err := CanonicalBusinessPayload(req); err != nil {
		return err
	}
	class, err := ManagedResourceClassForCreate(req)
	if err != nil {
		return err
	}
	// Presets freeze CPU and RAM per execution container. No other public
	// quantity or request/limit asymmetry can override that accepted selection.
	for _, quantities := range []map[string]string{req.GetResource().GetRequests(), req.GetResource().GetLimits()} {
		if len(quantities) != 2 {
			return fmt.Errorf("managed resources require exactly cpu and memory")
		}
		for _, key := range []string{"cpu", "memory"} {
			quantity, err := resource.ParseQuantity(quantities[key])
			if err != nil || quantity.Sign() <= 0 {
				return fmt.Errorf("managed %s must be positive", key)
			}
		}
	}
	for key, raw := range req.GetResource().GetRequests() {
		request, _ := resource.ParseQuantity(raw)
		limit, _ := resource.ParseQuantity(req.GetResource().GetLimits()[key])
		if request.Cmp(limit) != 0 {
			return fmt.Errorf("managed %s request and limit must match", key)
		}
	}
	devices := uint32(0)
	if class == inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU {
		devices = req.GetResource().GetGpu().GetDevicesPerReplica()
	}
	topology, err := NormalizeTopology(req.GetRuntime(), req.GetReplicas(), devices)
	if err != nil {
		return err
	}
	if class == inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU {
		gpu := req.GetResource().GetGpu()
		if gpu.GetReplicas() != topology.ExecutionReplicas || gpu.GetContainerName() != topology.ContainerName {
			return fmt.Errorf("GPU request differs from runtime topology")
		}
		if err := ValidateFrozenGPU(req.GetClusterId(), req.GetGpuExecution(), req.GetGpuDisplay(), gpu); err != nil {
			return err
		}
		if req.GetGpuExecution().GetResources()["volcano.sh/vgpu-number"] != strconv.FormatUint(uint64(gpu.GetDevicesPerReplica()), 10) {
			return fmt.Errorf("GPU execution device count differs from frozen selection")
		}
		if req.GetGpuDisplay().GetMode() == acceleratorv1.SupplyMode_SHARED_FIXED && devices != 1 {
			return fmt.Errorf("shared GPU requires one device per execution container")
		}
	}
	return nil
}

func ValidateManagedDelete(req *inferencev1.DeleteInferenceServiceRequest) error {
	if req == nil {
		return fmt.Errorf("delete request is required")
	}
	if err := ValidateCommandContext(req.GetContext()); err != nil {
		return err
	}
	if err := validateDeleteIdentity(req); err != nil {
		return err
	}
	if _, err := canonicalMessage(req.ProtoReflect(), false); err != nil {
		return err
	}
	return validateDeleteScope(req.GetManagedDeleteScope())
}

func validateDeleteIdentity(req *inferencev1.DeleteInferenceServiceRequest) error {
	if req.GetResourceId() != req.GetContext().GetResourceId() {
		return fmt.Errorf("DELETE resource differs from command context")
	}
	if err := canonicalUUID("create_operation_id", req.GetCreateOperationId()); err != nil {
		return err
	}
	if req.GetCreateOperationId() == req.GetContext().GetOperationId() {
		return fmt.Errorf("DELETE must use a new operation_id")
	}
	if req.GetExpectedGeneration() != 0 {
		return fmt.Errorf("managed DELETE does not accept expected_generation")
	}
	return nil
}

func validateDeleteScope(scope *inferencev1.ManagedDeleteScope) error {
	if scope == nil {
		return fmt.Errorf("managed_delete_scope is required")
	}
	if !validDigest(scope.GetCreateCommandDigest()) {
		return fmt.Errorf("create_command_digest must be a lowercase SHA-256 digest")
	}
	switch scope.GetResourceClass() {
	case inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU:
		if scope.GetClusterId() != "" {
			return fmt.Errorf("CPU DELETE cannot carry cluster_id")
		}
	case inferencev1.ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU:
		if err := canonicalUUID("cluster_id", scope.GetClusterId()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("managed_delete_scope resource_class is required")
	}
	return nil
}

// ValidateUpdatePatch validates an operation patch, never a synthetic CREATE.
// Resource/provider/endpoint/artifact attachments are rejected even off-mask.
// Off-mask business values are ignored by the digest and must not be merged.
func ValidateUpdatePatch(req *inferencev1.UpdateInferenceServiceRequest) error {
	if req == nil {
		return fmt.Errorf("update request is required")
	}
	if req.GetRequestId() == "" {
		return fmt.Errorf("request_id is required")
	}
	if err := canonicalUUID("resource_id", req.GetResourceId()); err != nil {
		return err
	}
	if req.GetExpectedGeneration() < 1 {
		return fmt.Errorf("expected_generation must be positive")
	}
	if req.GetResource() != nil || req.GetReplicas() != 0 || req.GetRuntime() != nil || req.GetModelArtifact() != nil || req.GetClusterId() != "" || req.GetGpuExecution() != nil || req.GetGpuDisplay() != nil {
		return fmt.Errorf("UPDATE cannot modify frozen resources, topology, provider, endpoint or artifact")
	}
	if _, err := canonicalMessage(req.ProtoReflect(), false); err != nil {
		return err
	}
	paths := req.GetUpdateMask().GetPaths()
	if len(paths) == 0 {
		return fmt.Errorf("update_mask is required and must not be empty")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			return fmt.Errorf("duplicate update_mask path %q", path)
		}
		seen[path] = true
		switch path {
		case "model_version_id":
			if err := canonicalUUID("model_version_id", req.GetModelVersionId()); err != nil {
				return err
			}
		case "served_model_name":
			if strings.TrimSpace(req.GetServedModelName()) == "" {
				return fmt.Errorf("selected served_model_name must not be empty")
			}
		case "engine":
			engine := req.GetEngine()
			if strings.TrimSpace(engine.GetType()) == "" || strings.TrimSpace(engine.GetImage()) == "" || strings.TrimSpace(engine.GetCommand()) == "" {
				return fmt.Errorf("selected engine requires type, image and complete command")
			}
		default:
			return fmt.Errorf("unsupported update_mask path %q", path)
		}
	}
	return nil
}

// ManagedCreateCommandHash is the new managed CPU/GPU owner command digest.
// Correlation request_id and context.request_digest are excluded; fixed R/C
// are bound explicitly, allowing the caller to calculate then attach digest.
func ManagedCreateCommandHash(req *inferencev1.CreateInferenceServiceRequest, tenantID, actorType, actorID string) (string, error) {
	if req == nil {
		return "", fmt.Errorf("create request is required")
	}
	identity, err := managedCommandIdentity(req.GetContext(), tenantID, actorType, actorID)
	if err != nil {
		return "", err
	}
	business, err := canonicalMessage(req.ProtoReflect(), true)
	if err != nil {
		return "", err
	}
	identity["schema"], identity["action"], identity["business"] = ManagedCreateSchema, "/inference.v1.InferenceServiceManager/CreateInferenceService", business
	return governanceHash(identity)
}

func ManagedDeleteCommandHash(req *inferencev1.DeleteInferenceServiceRequest, tenantID, actorType, actorID string) (string, error) {
	if req == nil {
		return "", fmt.Errorf("delete request is required")
	}
	identity, err := managedCommandIdentity(req.GetContext(), tenantID, actorType, actorID)
	if err != nil {
		return "", err
	}
	if err := validateDeleteIdentity(req); err != nil {
		return "", err
	}
	if err := validateDeleteScope(req.GetManagedDeleteScope()); err != nil {
		return "", err
	}
	if _, err := canonicalMessage(req.ProtoReflect(), false); err != nil {
		return "", err
	}
	scope, err := canonicalMessage(req.GetManagedDeleteScope().ProtoReflect(), false)
	if err != nil {
		return "", err
	}
	identity["schema"], identity["action"], identity["create_operation_id"], identity["managed_delete_scope"] = ManagedDeleteSchema, "/inference.v1.InferenceServiceManager/DeleteInferenceService", req.GetCreateOperationId(), scope
	return governanceHash(identity)
}

func ManagedUpdateCommandHash(req *inferencev1.UpdateInferenceServiceRequest, tenantID, actorType, actorID string) (string, error) {
	if err := ValidateUpdatePatch(req); err != nil {
		return "", err
	}
	if err := canonicalUUID("tenant_id", tenantID); err != nil {
		return "", err
	}
	if strings.TrimSpace(actorType) == "" || strings.TrimSpace(actorID) == "" {
		return "", fmt.Errorf("actor type and ID are required")
	}
	if req.GetContext() != nil {
		if req.GetContext().GetTenantId() != tenantID || req.GetContext().GetResourceId() != req.GetResourceId() {
			return "", fmt.Errorf("UPDATE context identity mismatch")
		}
	}
	paths := append([]string(nil), req.GetUpdateMask().GetPaths()...)
	sort.Strings(paths)
	patch := map[string]any{}
	for _, path := range paths {
		switch path {
		case "model_version_id":
			patch[path] = req.GetModelVersionId()
		case "served_model_name":
			patch[path] = req.GetServedModelName()
		case "engine":
			engine, err := canonicalMessage(req.GetEngine().ProtoReflect(), false)
			if err != nil {
				return "", err
			}
			patch[path] = engine
		}
	}
	return governanceHash(map[string]any{"schema": ManagedUpdateSchema, "tenant_id": tenantID, "actor_type": actorType, "actor_id": actorID, "owner": "ani-inference", "action": "/inference.v1.InferenceServiceManager/UpdateInferenceService", "resource_id": req.GetResourceId(), "expected_generation": strconv.FormatInt(req.GetExpectedGeneration(), 10), "update_mask": paths, "patch": patch})
}

func managedCommandIdentity(context *inferencev1.CommandContext, tenantID, actorType, actorID string) (map[string]any, error) {
	if context == nil || context.GetTenantId() != tenantID {
		return nil, fmt.Errorf("managed command tenant mismatch")
	}
	if len(context.ProtoReflect().GetUnknown()) != 0 {
		return nil, fmt.Errorf("command context contains unknown fields")
	}
	for name, value := range map[string]string{"tenant_id": tenantID, "resource_id": context.GetResourceId(), "operation_id": context.GetOperationId()} {
		if err := canonicalUUID(name, value); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(actorType) == "" || strings.TrimSpace(actorID) == "" {
		return nil, fmt.Errorf("actor type and ID are required")
	}
	return map[string]any{"tenant_id": tenantID, "actor_type": actorType, "actor_id": actorID, "owner": "ani-inference", "resource_id": context.GetResourceId(), "operation_id": context.GetOperationId()}, nil
}

func canonicalUUID(name, value string) error {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return fmt.Errorf("%s must be a canonical UUID", name)
	}
	return nil
}
