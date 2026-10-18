package postgres

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
)

func quantitySnapshot(t *testing.T, whole bool) *gpu.Snapshot {
	t.Helper()
	cluster, pool, profile := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request := &gpu.Request{ClusterID: cluster, PoolID: pool, ProfileID: profile, ProfileVersion: 1, Replicas: 2, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	mode := int32(acceleratorv1.SupplyMode_SHARED_FIXED)
	resources := map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "6144", "volcano.sh/vgpu-cores": "25"}
	if whole {
		mode = int32(acceleratorv1.SupplyMode_WHOLE_EXCLUSIVE)
		resources = map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "0", "volcano.sh/vgpu-cores": "100", "volcano.sh/vgpu-memory-percentage": "100"}
	}
	snapshot := &gpu.Snapshot{Request: request, Execution: &gpu.Execution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/model-key": "nvidia-test"}, Resources: resources, ExecutionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Display: &gpu.Display{ProfileID: profile, ProfileVersion: 1, ProfileName: "test", ModelKey: "nvidia-test", Mode: mode}, QuotaItems: []gpu.QuotaItem{{QuotaCode: "gpu.shared", Units: 12288}}}
	snapshot.ExecutionDigest = snapshot.Execution.ExecutionDigest
	if err := gpu.ValidateSnapshot(snapshot, request); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPostgresManagedGPUQuantityPlanRoundtrip(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "Shared2", true: "Whole2"}[whole], func(t *testing.T) {
			expected := quantitySnapshot(t, whole)
			raw, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			var loaded gpu.Snapshot
			if err := json.Unmarshal(raw, &loaded); err != nil {
				t.Fatal(err)
			}
			if err := gpu.ValidateSnapshot(&loaded, expected.Request); err != nil {
				t.Fatal(err)
			}
			if loaded.ExecutionDigest != expected.ExecutionDigest || loaded.Display.Mode != expected.Display.Mode || loaded.QuotaItems[0].Units != 12288 {
				t.Fatalf("quantity snapshot changed: %+v", loaded)
			}
			digest, err := gpu.SnapshotDigest(&loaded)
			if err != nil || len(digest) != 64 {
				t.Fatalf("snapshot digest=%q error=%v", digest, err)
			}
		})
	}
}

func TestPostgresManagedGPUQuantityRejectsWrongOriginalCharge(t *testing.T) {
	// Quota item quantities are ACC-owned. There is no caller-supplied
	// "original charge" field to forge or persist in the new contract.
	snapshot := quantitySnapshot(t, false)
	raw, err := json.Marshal(gpu.RefundContext{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsLegacyChargeField(raw) {
		t.Fatalf("legacy original charge leaked into context: %s", raw)
	}
}

func TestLegacyGPUSnapshotJSONMigratesToExecutionDisplay(t *testing.T) {
	request := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	raw := []byte(`{"request":{"cluster_id":"10000000-0000-4000-8000-000000000001","pool_id":"10000000-0000-4000-8000-000000000002","profile_id":"10000000-0000-4000-8000-000000000003","profile_version":1,"replicas":1,"devices_per_replica":1,"container_name":"kserve-container"},"profile":{"profile_id":"10000000-0000-4000-8000-000000000003","profile_version":1,"display_name":"legacy","spec":{"mode":2,"model_key":"nvidia-test"}},"runtime":{"scheduler_name":"volcano","queue_name":"gpu","node_labels":[{"key":"accelerator.ani.io/model-key","value":"nvidia-test"}],"limits_per_container":[{"key":"volcano.sh/vgpu-number","value":"1"}]},"resolution_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	snapshot, err := decodePersistedGPUSnapshot(raw, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := gpu.ValidateSnapshot(snapshot, request); err != nil {
		t.Fatal(err)
	}
	if snapshot.Execution.SchedulerName != "volcano" || snapshot.Display.ProfileName != "legacy" || snapshot.Execution.Resources["volcano.sh/vgpu-number"] != "1" {
		t.Fatalf("legacy snapshot not migrated: %+v", snapshot)
	}
}

func containsLegacyChargeField(raw []byte) bool {
	for _, field := range []string{"charge_id", "original_units", "gpu_charges", "original_charges"} {
		if bytes.Contains(raw, []byte(field)) {
			return true
		}
	}
	return false
}
