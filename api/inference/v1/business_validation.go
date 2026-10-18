package inferencev1

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"k8s.io/apimachinery/pkg/api/resource"
	validation "k8s.io/apimachinery/pkg/util/validation"
)

// ValidateKubernetesName validates the public inference service name against
// the DNS label accepted by KServe for the generated runtime object. Checking
// this at the API boundary prevents an asynchronous operation from being
// accepted only to fail later at the KServe admission webhook.
func ValidateKubernetesName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if errs := validation.IsDNS1123Label(name); len(errs) != 0 {
		return fmt.Errorf("name must be a lowercase DNS label: %s", errs[0])
	}
	return nil
}

// ValidateBusinessPayload checks the public business contract before quota
// acceptance. Trusted attachments are validated by the Inference receiver.
func ValidateBusinessPayload(req *CreateInferenceServiceRequest) error {
	if req == nil {
		return fmt.Errorf("name is required")
	}
	if err := ValidateKubernetesName(req.GetName()); err != nil {
		return err
	}
	if version, err := uuid.Parse(req.GetModelVersionId()); err != nil || version == uuid.Nil {
		return fmt.Errorf("model_version_id must be a UUID")
	}
	engine := req.GetEngine()
	if engine == nil || strings.TrimSpace(engine.GetType()) == "" || strings.TrimSpace(engine.GetImage()) == "" || strings.TrimSpace(engine.GetCommand()) == "" {
		return fmt.Errorf("engine type, image and command are required")
	}
	if req.GetReplicas() < 1 {
		return fmt.Errorf("replicas must be positive")
	}
	runtime := req.GetRuntime()
	provider := strings.ToLower(strings.TrimSpace(runtime.GetProvider()))
	if provider != "" && provider != "kserve" {
		return fmt.Errorf("runtime provider must be kserve")
	}
	pods := int64(req.GetReplicas())
	container := "kserve-container"
	switch runtime.GetMode() {
	case RuntimeMode_RUNTIME_MODE_UNSPECIFIED, RuntimeMode_RUNTIME_MODE_DEPLOYMENT:
		if runtime.GetWorkerReplicas() > 1 || runtime.GetWorkerReplicas() < 0 {
			return fmt.Errorf("deployment cannot specify multiple workers")
		}
	case RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET:
		if runtime.GetWorkerReplicas() < 1 {
			return fmt.Errorf("LWS worker_replicas must be positive")
		}
		pods *= int64(runtime.GetWorkerReplicas()) + 1
		container = "main"
	default:
		return fmt.Errorf("unsupported runtime mode")
	}
	if endpoint := runtime.GetEndpoint(); endpoint != nil {
		if endpoint.GetContainerPort() < 1 || endpoint.GetContainerPort() > 65535 || endpoint.GetServicePort() < 1 || endpoint.GetServicePort() > 65535 || endpoint.GetTargetPort() == "" {
			return fmt.Errorf("invalid endpoint ports")
		}
		if endpoint.GetProtocol() != "TCP" && endpoint.GetProtocol() != "UDP" && endpoint.GetProtocol() != "SCTP" {
			return fmt.Errorf("unsupported endpoint protocol")
		}
	}
	if context := req.GetContext(); context != nil {
		if err := ValidateCommandContext(context); err != nil {
			return err
		}
	}
	spec := req.GetResource()
	gpu := spec.GetGpu()
	if req.GetGpuExecution() != nil || req.GetGpuDisplay() != nil || req.GetClusterId() != "" {
		if gpu == nil {
			return fmt.Errorf("frozen GPU execution requires resource.gpu")
		}
		if err := ValidateFrozenGPU(req.GetClusterId(), req.GetGpuExecution(), req.GetGpuDisplay(), gpu); err != nil {
			return err
		}
	}
	if gpu != nil {
		// KServe v0.16's WorkloadSpec cannot carry the Volcano/HAMI pod
		// annotations, and its LWS metadata filter drops the queue annotation.
		// Reject before quota acceptance until the owner installs a supported
		// synchronous LWS projection hook.
		if runtime.GetMode() == RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET {
			return fmt.Errorf("managed GPU LWS creation requires a synchronous pre-create projection hook; KServe v0.16 does not provide it")
		}
		if pods > 16 || gpu.GetReplicas() != uint32(pods) || gpu.GetDevicesPerReplica() < 1 || gpu.GetContainerName() != container || gpu.GetProfileVersion() != 1 {
			return fmt.Errorf("GPU selection differs from fixed runtime topology")
		}
		for _, id := range []string{gpu.GetClusterId(), gpu.GetPoolId(), gpu.GetProfileId()} {
			parsed, err := uuid.Parse(id)
			if err != nil || parsed == uuid.Nil || parsed.String() != id {
				return fmt.Errorf("GPU selection IDs must be canonical UUIDs")
			}
		}
	}
	for _, quantities := range []map[string]string{spec.GetRequests(), spec.GetLimits()} {
		for key, raw := range quantities {
			if len(validation.IsQualifiedName(key)) != 0 {
				return fmt.Errorf("invalid resource name %q", key)
			}
			lowered := strings.ToLower(key)
			if strings.Contains(lowered, "gpu") || strings.Contains(lowered, "gaudi") {
				return fmt.Errorf("GPU quantities must use resource.gpu")
			}
			parsed, err := resource.ParseQuantity(strings.TrimSpace(raw))
			if err != nil || parsed.Sign() < 0 {
				return fmt.Errorf("invalid resource quantity for %s", key)
			}
		}
	}
	for key, raw := range spec.GetRequests() {
		if limit, exists := spec.GetLimits()[key]; exists {
			requestQuantity, _ := resource.ParseQuantity(strings.TrimSpace(raw))
			limitQuantity, _ := resource.ParseQuantity(strings.TrimSpace(limit))
			if requestQuantity.Cmp(limitQuantity) > 0 {
				return fmt.Errorf("request exceeds limit for %s", key)
			}
			if strings.Contains(key, "/") && !strings.HasPrefix(key, "kubernetes.io/") && requestQuantity.Cmp(limitQuantity) != 0 {
				return fmt.Errorf("extended resource request and limit differ")
			}
		}
	}
	return nil
}

// ValidateCommandContext checks the stable identity and digest carried by a
// Governance command. Credentials and tenant authorization are verified by
// the transport layer and are deliberately not represented here.
func ValidateCommandContext(context *CommandContext) error {
	if context == nil {
		return fmt.Errorf("command context is required")
	}
	for name, value := range map[string]string{
		"tenant_id": context.GetTenantId(), "resource_id": context.GetResourceId(), "operation_id": context.GetOperationId(),
	} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return fmt.Errorf("%s must be a canonical UUID", name)
		}
	}
	if !validDigest(context.GetRequestDigest()) {
		return fmt.Errorf("request_digest must be a lowercase SHA-256 digest")
	}
	return nil
}

// ValidateFrozenGPU validates the exact ACC execution snapshot. Inference
// never reconstructs scheduler fields or resource quantities from a profile.
func ValidateFrozenGPU(clusterID string, execution *acceleratorv1.GpuExecution, display *acceleratorv1.GpuDisplay, resourceGPU ...*GpuRequest) error {
	parsed, err := uuid.Parse(clusterID)
	if err != nil || parsed == uuid.Nil || parsed.String() != clusterID {
		return fmt.Errorf("cluster_id must be a canonical UUID")
	}
	if display == nil {
		return fmt.Errorf("gpu_display is required with frozen GPU execution")
	}
	if len(display.ProtoReflect().GetUnknown()) != 0 {
		return fmt.Errorf("gpu_display contains unknown fields")
	}
	if len(resourceGPU) > 1 {
		return fmt.Errorf("only one resource.gpu snapshot is allowed")
	}
	if len(resourceGPU) == 1 && resourceGPU[0] != nil {
		gpu := resourceGPU[0]
		if gpu.GetClusterId() != clusterID {
			return fmt.Errorf("frozen GPU cluster_id must match resource.gpu.cluster_id")
		}
		if gpu.GetProfileId() != display.GetProfileId() || gpu.GetProfileVersion() != display.GetProfileVersion() {
			return fmt.Errorf("GPU profile differs between resource.gpu and gpu_display")
		}
	}
	profileID, err := uuid.Parse(display.GetProfileId())
	if err != nil || profileID == uuid.Nil || profileID.String() != display.GetProfileId() {
		return fmt.Errorf("gpu_display.profile_id must be a canonical UUID")
	}
	if display.GetProfileVersion() == 0 || strings.TrimSpace(display.GetProfileName()) == "" || strings.TrimSpace(display.GetModelKey()) == "" {
		return fmt.Errorf("gpu_display profile_version, profile_name and model_key are required")
	}
	if display.GetMode() != acceleratorv1.SupplyMode_SHARED_FIXED && display.GetMode() != acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE {
		return fmt.Errorf("gpu_display.mode is required")
	}
	if err := gpuprotocol.ValidateGpuExecution(execution); err != nil {
		return fmt.Errorf("gpu_execution: %w", err)
	}
	if execution.GetNodeSelector()["accelerator.ani.io/model-key"] != display.GetModelKey() {
		return fmt.Errorf("gpu_display.model_key differs from gpu_execution selector")
	}
	switch display.GetMode() {
	case acceleratorv1.SupplyMode_SHARED_FIXED:
		if execution.GetResources()["volcano.sh/vgpu-number"] != "1" || execution.GetResources()["volcano.sh/vgpu-cores"] == "100" {
			return fmt.Errorf("shared GPU display differs from gpu_execution resources")
		}
	case acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE:
		if execution.GetResources()["volcano.sh/vgpu-cores"] != "100" {
			return fmt.Errorf("whole GPU display differs from gpu_execution resources")
		}
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
