package gpu

import (
	"testing"
)

func validRequest() *Request {
	return &Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
}
func validSnapshot() *Snapshot {
	return &Snapshot{Request: validRequest(), Execution: &Execution{SchedulerName: "volcano", QueueName: "queue", NodeSelector: map[string]string{"a": "b"}, Resources: map[string]string{"nvidia.com/gpu": "1"}, ExecutionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Display: &Display{ProfileID: validRequest().ProfileID, ProfileVersion: 1, ProfileName: "shared", ModelKey: "model", Mode: 2}}
}
func TestValidateExecutionSnapshot(t *testing.T) {
	s := validSnapshot()
	if err := ValidateSnapshot(s, s.Request); err != nil {
		t.Fatal(err)
	}
	s.Execution.QueueName = ""
	if err := ValidateSnapshot(s, s.Request); err != nil {
		t.Fatal("queue is optional in ACC execution: ", err)
	}
}

func TestValidateRequestAcceptsPhaseOneShape(t *testing.T) {
	for _, devices := range []uint32{1, 2, 4} {
		r := validRequest()
		r.DevicesPerReplica = devices
		if err := ValidateRequest(r); err != nil {
			t.Fatalf("ValidateRequest(%d): %v", devices, err)
		}
	}
}

func TestValidateRequestRejectsWrongContainerShape(t *testing.T) {
	r := validRequest()
	r.DevicesPerReplica = 0
	if err := ValidateRequest(r); err == nil {
		t.Fatal("accepted devices_per_replica=0")
	}
}

func TestValidateTopologyUsesRuntimeContainerName(t *testing.T) {
	r := validRequest()
	if err := ValidateTopology(r, 1, "deployment", 0); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	r.ContainerName, r.Replicas = "main", 2
	if err := ValidateTopology(r, 1, "leader_worker_set", 1); err != nil {
		t.Fatalf("leader-worker: %v", err)
	}
	if err := ValidateTopology(r, 2, "leader_worker_set", 3); err == nil {
		t.Fatal("accepted GPU replica count that differs from pod topology")
	}
}

func TestTotalPodsForLeaderWorkerSet(t *testing.T) {
	got, err := TotalPods(2, "leader_worker_set", 3)
	if err != nil || got != 8 {
		t.Fatalf("TotalPods=%d error=%v; want 8", got, err)
	}
}

func TestValidateTopologyRequiresMatchingReplicaCount(t *testing.T) {
	if err := ValidateTopology(validRequest(), 2, "deployment", 0); err == nil {
		t.Fatal("accepted mismatched GPU/runtime replicas")
	}
}

func TestSnapshotDigestChangesWhenExecutionChanges(t *testing.T) {
	s := validSnapshot()
	a, err := SnapshotDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Execution.SchedulerName = "other"
	b, err := SnapshotDigest(s)
	if err != nil || a == b {
		t.Fatalf("execution change did not affect digest: %s %s (%v)", a, b, err)
	}
}

func TestSnapshotDigestChangesWithExecution(t *testing.T) {
	s := validSnapshot()
	a, err := SnapshotDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	s.Execution.SchedulerName = "other"
	b, err := SnapshotDigest(s)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("digest did not commit execution")
	}
}
func TestValidateTopology(t *testing.T) {
	if err := ValidateTopology(validRequest(), 1, "deployment", 1); err != nil {
		t.Fatal(err)
	}
	r := validRequest()
	r.Replicas = 2
	r.ContainerName = "main"
	if err := ValidateTopology(r, 1, "leader_worker_set", 1); err != nil {
		t.Fatal(err)
	}
}
