package postgres

import (
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
)

func TestValidateManagedGPUReleasePodsRequiresCompleteUniqueIdentities(t *testing.T) {
	valid := gpu.PodRef{ClusterID: "cluster", Namespace: "ani-system", Name: "model-0", UID: "uid-1", ContainerName: "kserve-container"}
	for name, pods := range map[string][]gpu.PodRef{
		"empty":         nil,
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
}
