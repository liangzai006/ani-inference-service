package postgres

import (
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
)

func TestValidateManagedGPUReleasePodsRequiresCompleteUniqueIdentities(t *testing.T) {
	valid := gpu.PodRef{ClusterID: "cluster", Namespace: "ani-system", Name: "model-0", UID: "uid-1", ContainerName: "kserve-container"}
	for name, pods := range map[string][]gpu.PodRef{
		"missing uid":   {{ClusterID: "cluster", Namespace: "ani-system", Name: "model-0", ContainerName: "kserve-container"}},
		"duplicate uid": {valid, valid},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateManagedGPUReleasePods(pods); err == nil {
				t.Fatal("validateManagedGPUReleasePods() accepted invalid scope")
			}
		})
	}
	if err := validateManagedGPUReleasePods([]gpu.PodRef{valid}); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
	if err := validateManagedGPUReleasePods(nil); err != nil {
		t.Fatalf("empty scope rejected: %v", err)
	}
}

func TestManagedGPUReleaseScopePersistsStableSealWithEmptyPods(t *testing.T) {
	const sealedAt = "2026-10-18T00:00:00.000000000Z"
	payload, err := encodeManagedGPUReleaseScope(sealedAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotSeal, gotPods, err := decodeManagedGPUReleaseScope(payload)
	if err != nil {
		t.Fatal(err)
	}
	if gotSeal != sealedAt || gotPods == nil || len(gotPods) != 0 {
		t.Fatalf("seal=%q pods=%#v", gotSeal, gotPods)
	}
}

func TestMergeManagedGPUReleasePodsPreservesAllGenerations(t *testing.T) {
	first := gpu.PodRef{ClusterID: "cluster", Namespace: "ani-system", Name: "model-0", UID: "uid-1", ContainerName: "main"}
	second := gpu.PodRef{ClusterID: "cluster", Namespace: "ani-system", Name: "model-1", UID: "uid-2", ContainerName: "main"}
	merged, err := mergeManagedGPUReleasePods([]gpu.PodRef{first}, []gpu.PodRef{second, first})
	if err != nil || len(merged) != 2 || merged[0].UID != first.UID || merged[1].UID != second.UID {
		t.Fatalf("merged=%#v err=%v", merged, err)
	}
	changed := first
	changed.Name = "replacement"
	if _, err := mergeManagedGPUReleasePods([]gpu.PodRef{first}, []gpu.PodRef{changed}); err == nil {
		t.Fatal("changed identity for existing Pod UID accepted")
	}
}
