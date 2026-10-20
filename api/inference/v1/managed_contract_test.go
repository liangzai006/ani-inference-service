package inferencev1

import (
	"math"
	"strings"
	"testing"

	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const managedTenant = "11111111-1111-4111-8111-111111111111"
const managedResource = "22222222-2222-4222-8222-222222222222"
const managedCreate = "33333333-3333-4333-8333-333333333333"
const managedDelete = "44444444-4444-4444-8444-444444444444"

func managedCPU() *CreateInferenceServiceRequest {
	req := validBusinessRequest()
	req.Resource = &ResourceSpec{Requests: map[string]string{"cpu": "1500m", "memory": "1536Mi"}, Limits: map[string]string{"cpu": "1500m", "memory": "1536Mi"}}
	req.Context = &CommandContext{TenantId: managedTenant, ResourceId: managedResource, OperationId: managedCreate, RequestDigest: strings.Repeat("a", 64)}
	return req
}

func TestManagedIdentityIsIndependentOfGPU(t *testing.T) {
	req := managedCPU()
	if err := ValidateManagedCreate(req); err != nil {
		t.Fatal(err)
	}
	if class, err := ManagedResourceClassForCreate(req); err != nil || class != ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU {
		t.Fatalf("class=%v err=%v", class, err)
	}
	for _, mutate := range []func(*CreateInferenceServiceRequest){
		func(r *CreateInferenceServiceRequest) { r.ClusterId = managedTenant },
		func(r *CreateInferenceServiceRequest) { r.GpuDisplay = &acceleratorv1.GpuDisplay{} },
		func(r *CreateInferenceServiceRequest) { r.GpuExecution = &acceleratorv1.GpuExecution{} },
		func(r *CreateInferenceServiceRequest) { r.Resource.Limits["cpu"] = "2" },
		func(r *CreateInferenceServiceRequest) { r.Resource.Requests["example.com/fpga"] = "1" },
		func(r *CreateInferenceServiceRequest) { r.ModelArtifact = &ModelArtifact{Reference: "untrusted"} },
	} {
		candidate := proto.Clone(req).(*CreateInferenceServiceRequest)
		mutate(candidate)
		if err := ValidateManagedCreate(candidate); err == nil {
			t.Fatalf("invalid mixed/resource override accepted: %v", candidate)
		}
	}
}

func TestManagedTopologyIsAuthoritative(t *testing.T) {
	topology, err := NormalizeTopology(nil, 3, 2)
	if err != nil || topology.ExecutionReplicas != 3 || topology.DevicesPerReplica != 2 || topology.TotalDevices != 6 || topology.ContainerName != "kserve-container" {
		t.Fatalf("topology=%v err=%v", topology, err)
	}
	lws := &RuntimeSpec{Mode: RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 2}
	cpu, err := NormalizeTopology(lws, 2, 0)
	if err != nil || cpu.ExecutionReplicas != 6 || cpu.TotalDevices != 0 || cpu.ContainerName != "main" {
		t.Fatalf("CPU LWS=%v err=%v", cpu, err)
	}
	for _, test := range []struct {
		runtime  *RuntimeSpec
		replicas int32
		devices  uint32
	}{
		{nil, 0, 0}, {nil, 17, 1}, {lws, 1, 1}, {lws, math.MaxInt32, 0},
		{&RuntimeSpec{Mode: RuntimeMode_RUNTIME_MODE_DEPLOYMENT, WorkerReplicas: 2}, 1, 1},
		{&RuntimeSpec{Mode: RuntimeMode(99)}, 1, 0},
	} {
		if _, err := NormalizeTopology(test.runtime, test.replicas, test.devices); err == nil {
			t.Fatalf("unsupported topology accepted: %+v", test)
		}
	}
	if _, err := NormalizeTopology(nil, 16, math.MaxUint32); err != nil {
		t.Fatalf("valid large total unexpectedly overflowed: %v", err)
	}
}

func TestManagedGPURequiresCompleteFrozenDeviceCount(t *testing.T) {
	req := managedCPU()
	req.Resource.Gpu = &GpuRequest{ClusterId: managedTenant, PoolId: managedTenant, ProfileId: managedTenant, ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	req.ClusterId = managedTenant
	req.GpuExecution = validGPUExecution(t)
	req.GpuDisplay = &acceleratorv1.GpuDisplay{ProfileId: managedTenant, ProfileVersion: 1, ProfileName: "t4", ModelKey: "t4", Mode: acceleratorv1.SupplyMode_SHARED_FIXED}
	if err := ValidateManagedCreate(req); err != nil {
		t.Fatal(err)
	}
	req.Resource.Gpu.DevicesPerReplica = 2
	if err := ValidateManagedCreate(req); err == nil {
		t.Fatal("shared multi-device or execution-count mismatch accepted")
	}
	req.GpuExecution.Resources["volcano.sh/vgpu-number"] = "2"
	req.GpuExecution.Resources["volcano.sh/vgpu-cores"] = "100"
	digest, err := gpuprotocol.GpuExecutionDigest(req.GpuExecution)
	if err != nil {
		t.Fatal(err)
	}
	req.GpuExecution.ExecutionDigest = digest
	req.GpuDisplay.Mode = acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE
	if err := ValidateManagedCreate(req); err != nil {
		t.Fatalf("whole N=2: %v", err)
	}
}

func TestManagedCreateHashBindsFixedIdentityAndKeepsLegacyVector(t *testing.T) {
	req := managedCPU()
	req.Context.RequestDigest = ""
	hash, err := ManagedCreateCommandHash(req, managedTenant, "user", managedTenant)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "1dbc5eef899d779503667ada533a567d05be1dd94cb79fa939f491b5e9c405bd" {
		t.Fatalf("managed CREATE fixed digest changed: %s", hash)
	}
	req.RequestId = "correlation"
	req.Context.RequestDigest = hash
	if replay, err := ManagedCreateCommandHash(req, managedTenant, "user", managedTenant); err != nil || replay != hash {
		t.Fatalf("self/correlation changed digest: %s %v", replay, err)
	}
	if err := ValidateManagedCreate(req); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*CreateInferenceServiceRequest){
		func(r *CreateInferenceServiceRequest) { r.Context.ResourceId = managedDelete },
		func(r *CreateInferenceServiceRequest) { r.Context.OperationId = managedDelete },
		func(r *CreateInferenceServiceRequest) { r.Engine.Command = "different complete command" },
	} {
		candidate := proto.Clone(req).(*CreateInferenceServiceRequest)
		mutate(candidate)
		other, err := ManagedCreateCommandHash(candidate, managedTenant, "user", managedTenant)
		if err != nil || other == hash {
			t.Fatalf("identity/content unbound: %s %v", other, err)
		}
	}
	if _, err := ManagedCreateCommandHash(req, managedResource, "user", managedTenant); err == nil {
		t.Fatal("cross-tenant hash accepted")
	}
	// The published legacy GPU-only helper remains unavailable to CPU; the
	// existing fixed business-byte vector remains in business_payload_test.go.
	if _, err := ManagedCreateRequestHash(req, managedTenant, "user", managedTenant); err == nil {
		t.Fatal("legacy GPU schema silently changed to CPU")
	}
	t.Logf("managed CPU fixed command digest=%s", hash)
}

func managedDeleteRequest() *DeleteInferenceServiceRequest {
	return &DeleteInferenceServiceRequest{ResourceId: managedResource, CreateOperationId: managedCreate, Context: &CommandContext{TenantId: managedTenant, ResourceId: managedResource, OperationId: managedDelete}, ManagedDeleteScope: &ManagedDeleteScope{ResourceClass: ManagedResourceClass_MANAGED_RESOURCE_CLASS_CPU, CreateCommandDigest: strings.Repeat("a", 64)}}
}

func TestManagedDeleteHashBindsScopeAndCurrentActor(t *testing.T) {
	req := managedDeleteRequest()
	hash, err := ManagedDeleteCommandHash(req, managedTenant, "user", managedResource)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "5e3c27164e81d1cae398aed217e132ac0b33da44451077ac2b4ba247beb48f41" {
		t.Fatalf("managed DELETE fixed digest changed: %s", hash)
	}
	req.Context.RequestDigest = hash
	if err := ValidateManagedDelete(req); err != nil {
		t.Fatal(err)
	}
	if req.ProtoReflect().Descriptor().Fields().ByName("managed_delete_scope").Number() != 8 {
		t.Fatal("DELETE scope changed field number")
	}
	for _, mutate := range []func(*DeleteInferenceServiceRequest){
		func(r *DeleteInferenceServiceRequest) { r.Context.OperationId = managedTenant },
		func(r *DeleteInferenceServiceRequest) {
			r.ManagedDeleteScope.CreateCommandDigest = strings.Repeat("b", 64)
		},
		func(r *DeleteInferenceServiceRequest) {
			r.ManagedDeleteScope.ResourceClass = ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU
			r.ManagedDeleteScope.ClusterId = managedTenant
		},
	} {
		candidate := proto.Clone(req).(*DeleteInferenceServiceRequest)
		mutate(candidate)
		other, err := ManagedDeleteCommandHash(candidate, managedTenant, "user", managedResource)
		if err != nil || other == hash {
			t.Fatalf("scope/DELETE identity unbound: %s %v", other, err)
		}
	}
	actorHash, err := ManagedDeleteCommandHash(req, managedTenant, "user", managedTenant)
	if err != nil || actorHash == hash {
		t.Fatal("current actor unbound")
	}
	for _, mutate := range []func(*DeleteInferenceServiceRequest){
		func(r *DeleteInferenceServiceRequest) { r.ManagedDeleteScope = nil },
		func(r *DeleteInferenceServiceRequest) { r.ManagedDeleteScope.ClusterId = managedTenant },
		func(r *DeleteInferenceServiceRequest) {
			r.ManagedDeleteScope.ResourceClass = ManagedResourceClass_MANAGED_RESOURCE_CLASS_GPU
		},
		func(r *DeleteInferenceServiceRequest) { r.ManagedDeleteScope.CreateCommandDigest = "bad" },
		func(r *DeleteInferenceServiceRequest) { r.ResourceId = managedTenant },
		func(r *DeleteInferenceServiceRequest) { r.ExpectedGeneration = 1 },
	} {
		candidate := proto.Clone(req).(*DeleteInferenceServiceRequest)
		mutate(candidate)
		if err := ValidateManagedDelete(candidate); err == nil {
			t.Fatalf("invalid close delegation accepted: %v", candidate)
		}
	}
	t.Logf("managed CPU delete fixed digest=%s", hash)
}

func validPatch() *UpdateInferenceServiceRequest {
	return &UpdateInferenceServiceRequest{RequestId: "update-key", ResourceId: managedResource, ExpectedGeneration: 2, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"served_model_name"}}, ServedModelName: "new-name"}
}

func TestUpdatePatchWhitelistAndDigest(t *testing.T) {
	req := validPatch()
	if err := ValidateUpdatePatch(req); err != nil {
		t.Fatal(err)
	}
	hash, err := ManagedUpdateCommandHash(req, managedTenant, "user", managedResource)
	if err != nil {
		t.Fatal(err)
	}
	req.ModelVersionId = "off-mask-ignored"
	req.Engine = &EngineSpec{Image: "off-mask"}
	other, err := ManagedUpdateCommandHash(req, managedTenant, "user", managedResource)
	if err != nil || other != hash {
		t.Fatalf("off-mask data changed patch hash: %v", err)
	}
	req = validPatch()
	req.UpdateMask.Paths = []string{"engine", "served_model_name"}
	req.Engine = &EngineSpec{Type: "vllm", Image: "new:v2", Command: "new complete command"}
	ordered, err := ManagedUpdateCommandHash(req, managedTenant, "user", managedResource)
	if err != nil {
		t.Fatal(err)
	}
	req.UpdateMask.Paths = []string{"served_model_name", "engine"}
	reordered, _ := ManagedUpdateCommandHash(req, managedTenant, "user", managedResource)
	if ordered != reordered {
		t.Fatal("mask order changed semantic patch digest")
	}
	for _, mutate := range []func(*UpdateInferenceServiceRequest){
		func(r *UpdateInferenceServiceRequest) { r.UpdateMask = nil },
		func(r *UpdateInferenceServiceRequest) { r.UpdateMask.Paths = []string{"engine.image"} },
		func(r *UpdateInferenceServiceRequest) {
			r.UpdateMask.Paths = []string{"engine"}
			r.Engine = &EngineSpec{Type: "vllm", Image: "missing command"}
		},
		func(r *UpdateInferenceServiceRequest) { r.UpdateMask.Paths = []string{"name"} },
		func(r *UpdateInferenceServiceRequest) { r.UpdateMask.Paths = []string{"model_version_id"} },
		func(r *UpdateInferenceServiceRequest) { r.ServedModelName = "" },
		func(r *UpdateInferenceServiceRequest) { r.Resource = &ResourceSpec{} },
		func(r *UpdateInferenceServiceRequest) { r.Runtime = &RuntimeSpec{} },
		func(r *UpdateInferenceServiceRequest) { r.Replicas = 1 },
		func(r *UpdateInferenceServiceRequest) { r.ModelArtifact = &ModelArtifact{} },
		func(r *UpdateInferenceServiceRequest) { r.GpuExecution = &acceleratorv1.GpuExecution{} },
	} {
		candidate := validPatch()
		mutate(candidate)
		if err := ValidateUpdatePatch(candidate); err == nil {
			t.Fatalf("invalid patch accepted: %v", candidate)
		}
	}
}
