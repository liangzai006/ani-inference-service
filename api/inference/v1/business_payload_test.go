package inferencev1

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestBusinessCanonicalFixedVectorAndRoundtrip(t *testing.T) {
	req := &CreateInferenceServiceRequest{RequestId: "dynamic", Name: "<&中文>", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1, Resource: &ResourceSpec{Limits: map[string]string{"memory": "8Gi", "cpu": "2"}}, Engine: &EngineSpec{Type: "vllm", Image: "engine:v1", Command: "serve"}, Runtime: &RuntimeSpec{Mode: RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 1}}
	const expected = `{"cluster_id":"","engine":{"command":"serve","image":"engine:v1","type":"vllm"},"gpu_display":null,"gpu_execution":null,"model_artifact":null,"model_version_id":"11111111-1111-4111-8111-111111111111","name":"<&中文>","replicas":"1","resource":{"gpu":null,"limits":{"cpu":"2","memory":"8Gi"},"requests":{}},"runtime":{"endpoint":null,"mode":"2","provider":"","worker_replicas":"1"},"served_model_name":""}`
	raw, err := CanonicalBusinessPayload(req)
	if err != nil || string(raw) != expected {
		t.Fatalf("canonical=%s error=%v", raw, err)
	}
	digest, err := BusinessPayloadDigest(req)
	sum := sha256.Sum256([]byte(expected))
	if err != nil || digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest=%s error=%v", digest, err)
	}
	t.Logf("fixed business digest=%s", digest)
	decoded, err := DecodeBusinessPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	req.RequestId = ""
	if !proto.Equal(req, decoded) {
		t.Fatalf("decoded business differs: %+v", decoded)
	}
	req.RequestId = "another"
	req.ProtoReflect().SetUnknown([]byte{0x52, 0x01, 0x00}) // retired field 10
	excluded, err := CanonicalBusinessPayload(req)
	if err == nil || excluded != nil {
		t.Fatalf("retired owner attachment was accepted: %s error=%v", excluded, err)
	}
	req.ProtoReflect().SetUnknown(nil)
	req.Context = &CommandContext{TenantId: "11111111-1111-4111-8111-111111111111", ResourceId: "22222222-2222-4222-8222-222222222222", OperationId: "33333333-3333-4333-8333-333333333333", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	contextDigest, err := CanonicalBusinessPayload(req)
	if err != nil || !bytes.Equal(raw, contextDigest) {
		t.Fatalf("command context contaminated business digest: %s error=%v", contextDigest, err)
	}
	req.Context = nil
	req.Name = "changed"
	changed, _ := BusinessPayloadDigest(req)
	if changed == digest {
		t.Fatal("business changes did not alter digest")
	}
}

func TestFrozenGPUValidationRejectsDigestAndClusterMismatches(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	execution := &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "default", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "group", "accelerator.ani.io/model-key": "t4", "accelerator.ani.io/baseline-id": "baseline"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "8192", "volcano.sh/vgpu-cores": "50"}}
	digest, err := gpuprotocol.GpuExecutionDigest(execution)
	if err != nil {
		t.Fatal(err)
	}
	execution.ExecutionDigest = digest
	if err := ValidateFrozenGPU(id, execution, &acceleratorv1.GpuDisplay{ProfileId: id, ProfileVersion: 1, ProfileName: "t4", ModelKey: "t4", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}); err != nil {
		t.Fatal(err)
	}
	execution.Resources["cpu"] = "1"
	if err := ValidateFrozenGPU(id, execution, &acceleratorv1.GpuDisplay{}); err == nil {
		t.Fatal("non-GPU execution resource key accepted")
	}
	delete(execution.Resources, "cpu")
	execution.ExecutionDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := ValidateFrozenGPU(id, execution, &acceleratorv1.GpuDisplay{}); err == nil {
		t.Fatal("execution digest mismatch accepted")
	}
	execution.ExecutionDigest = digest
	if err := ValidateFrozenGPU("cluster-not-a-uuid", execution, &acceleratorv1.GpuDisplay{}); err == nil {
		t.Fatal("invalid cluster fence accepted")
	}
}

func TestFrozenGPUValidationRequiresDisplayExecutionConsistency(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	execution := validGPUExecution(t)
	display := &acceleratorv1.GpuDisplay{ProfileId: id, ProfileVersion: 1, ProfileName: "t4", ModelKey: "t4", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}
	if err := ValidateFrozenGPU(id, execution, display, &GpuRequest{ClusterId: "22222222-2222-4222-8222-222222222222", ProfileId: id, ProfileVersion: 1}); err == nil {
		t.Fatal("resource GPU cluster mismatch accepted")
	}
	display.ModelKey = "a100"
	if err := ValidateFrozenGPU(id, execution, display); err == nil {
		t.Fatal("display model key mismatch accepted")
	}
	display.ModelKey = "t4"
	execution.Resources["volcano.sh/vgpu-cores"] = "100"
	digest, err := gpuprotocol.GpuExecutionDigest(execution)
	if err != nil {
		t.Fatal(err)
	}
	execution.ExecutionDigest = digest
	if err := ValidateFrozenGPU(id, execution, display); err == nil {
		t.Fatal("shared display with whole-device execution accepted")
	}
	display.Mode = acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE
	if err := ValidateFrozenGPU(id, execution, display); err != nil {
		t.Fatalf("whole display with whole-device execution rejected: %v", err)
	}
}

func TestCommandContextValidationRequiresCanonicalIdentityAndDigest(t *testing.T) {
	context := &CommandContext{TenantId: "11111111-1111-4111-8111-111111111111", ResourceId: "22222222-2222-4222-8222-222222222222", OperationId: "33333333-3333-4333-8333-333333333333", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := ValidateCommandContext(context); err != nil {
		t.Fatal(err)
	}
	context.RequestDigest = "A"
	if err := ValidateCommandContext(context); err == nil {
		t.Fatal("noncanonical request digest accepted")
	}
}

func TestBusinessValidationChecksOptionalCommandContext(t *testing.T) {
	req := validBusinessRequest()
	if err := ValidateBusinessPayload(req); err != nil {
		t.Fatalf("direct request without command context rejected: %v", err)
	}
	req.Context = &CommandContext{TenantId: "bad", ResourceId: "22222222-2222-4222-8222-222222222222", OperationId: "33333333-3333-4333-8333-333333333333", RequestDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := ValidateBusinessPayload(req); err == nil {
		t.Fatal("invalid command context accepted when explicitly supplied")
	}
}

func TestBusinessValidationAllowsNonGPUExtendedResourceWithGPUSelection(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	req := validBusinessRequest()
	req.Resource = &ResourceSpec{
		Requests: map[string]string{"example.com/fpga": "1"},
		Limits:   map[string]string{"example.com/fpga": "1"},
		Gpu:      &GpuRequest{ClusterId: id, PoolId: id, ProfileId: id, ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"},
	}
	if err := ValidateBusinessPayload(req); err != nil {
		t.Fatalf("ordinary extended resource rejected: %v", err)
	}
}

func TestBusinessValidationFencesFrozenGPUToResourceCluster(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	req := validBusinessRequest()
	req.ClusterId = id
	req.Resource = &ResourceSpec{Gpu: &GpuRequest{ClusterId: "22222222-2222-4222-8222-222222222222", PoolId: id, ProfileId: id, ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}}
	req.GpuExecution = validGPUExecution(t)
	req.GpuDisplay = &acceleratorv1.GpuDisplay{ProfileId: id, ProfileVersion: 1, ProfileName: "t4", ModelKey: "t4", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}
	if err := ValidateBusinessPayload(req); err == nil {
		t.Fatal("frozen GPU with mismatched resource cluster accepted")
	}
}

func validBusinessRequest() *CreateInferenceServiceRequest {
	return &CreateInferenceServiceRequest{
		Name: "valid", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1,
		Engine: &EngineSpec{Type: "vllm", Image: "engine:v1", Command: "serve"}, Resource: &ResourceSpec{},
	}
}

func validGPUExecution(t *testing.T) *acceleratorv1.GpuExecution {
	t.Helper()
	e := &acceleratorv1.GpuExecution{SchedulerName: "volcano", QueueName: "default", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "group", "accelerator.ani.io/model-key": "t4", "accelerator.ani.io/baseline-id": "baseline"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "8192", "volcano.sh/vgpu-cores": "50"}}
	digest, err := gpuprotocol.GpuExecutionDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	e.ExecutionDigest = digest
	return e
}

func TestBusinessRejectsUnknownAndNoncanonicalPayload(t *testing.T) {
	req := &CreateInferenceServiceRequest{}
	req.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
	if _, err := CanonicalBusinessPayload(req); err == nil {
		t.Fatal("unknown business fields accepted")
	}
	if _, err := DecodeBusinessPayload([]byte(`{"name":"partial"}`)); err == nil {
		t.Fatal("noncanonical partial payload accepted")
	}
}

func TestBusinessValidationRejectsResourcesBeforeQuota(t *testing.T) {
	req := &CreateInferenceServiceRequest{Name: "valid", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1, Engine: &EngineSpec{Type: "vllm", Image: "engine:v1", Command: "serve"}, Resource: &ResourceSpec{Requests: map[string]string{"cpu": "3"}, Limits: map[string]string{"cpu": "2"}}}
	if err := ValidateBusinessPayload(req); err == nil {
		t.Fatal("request above limit accepted")
	}
	req.Resource.Requests["cpu"] = "invalid"
	if err := ValidateBusinessPayload(req); err == nil {
		t.Fatal("invalid quantity accepted")
	}
	req.Resource.Requests["cpu"] = "1"
	if err := ValidateBusinessPayload(req); err != nil {
		t.Fatal(err)
	}
}

func TestValidateKubernetesNameRejectsKServeInvalidNames(t *testing.T) {
	for _, name := range []string{"qwen2-0.5b-infer-e2e", "Qwen", "name_with_underscore", "name."} {
		if err := ValidateKubernetesName(name); err == nil {
			t.Fatalf("name %q accepted; want Kubernetes DNS label validation", name)
		}
	}
	for _, name := range []string{"qwen2-05b-infer-e2e", "service-1"} {
		if err := ValidateKubernetesName(name); err != nil {
			t.Fatalf("name %q rejected: %v", name, err)
		}
	}
}

func TestBusinessValidationFailsClosedForManagedLWSOnly(t *testing.T) {
	req := &CreateInferenceServiceRequest{Name: "distributed", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1, Engine: &EngineSpec{Type: "vllm", Image: "engine:v1", Command: "serve"}, Runtime: &RuntimeSpec{Mode: RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 1}, Resource: &ResourceSpec{}}
	if err := ValidateBusinessPayload(req); err != nil {
		t.Fatalf("CPU LWS changed: %v", err)
	}
	req.Resource.Gpu = &GpuRequest{ClusterId: req.ModelVersionId, PoolId: req.ModelVersionId, ProfileId: req.ModelVersionId, ProfileVersion: 1, Replicas: 2, DevicesPerReplica: 1, ContainerName: "main"}
	if err := ValidateBusinessPayload(req); err == nil {
		t.Fatal("GPU LWS accepted before a supported synchronous projection exists")
	}
}

func TestBusinessValidationGPUQuantityContract(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	req := &CreateInferenceServiceRequest{Name: "quantity", ModelVersionId: id, Replicas: 1, Engine: &EngineSpec{Type: "vllm", Image: "engine:v1", Command: "serve"}, Runtime: &RuntimeSpec{Mode: RuntimeMode_RUNTIME_MODE_DEPLOYMENT}, Resource: &ResourceSpec{Gpu: &GpuRequest{ClusterId: id, PoolId: id, ProfileId: id, ProfileVersion: 1, Replicas: 1, ContainerName: "kserve-container"}}}
	for _, devices := range []uint32{1, 2, 4} {
		req.Resource.Gpu.DevicesPerReplica = devices
		if err := ValidateBusinessPayload(req); err != nil {
			t.Fatalf("legal preliminary count %d: %v", devices, err)
		}
	}
	for _, devices := range []uint32{0} {
		req.Resource.Gpu.DevicesPerReplica = devices
		if err := ValidateBusinessPayload(req); err == nil {
			t.Fatalf("unsupported count %d accepted", devices)
		}
	}
	req.Runtime.Mode, req.Runtime.WorkerReplicas = RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, 1
	req.Resource.Gpu.Replicas, req.Resource.Gpu.ContainerName = 2, "main"
	for _, devices := range []uint32{1, 2} {
		req.Resource.Gpu.DevicesPerReplica = devices
		if err := ValidateBusinessPayload(req); err == nil {
			t.Fatalf("GPU LWS became supported for count %d", devices)
		}
	}
}
