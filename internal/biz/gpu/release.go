package gpu

import (
	"context"
	"fmt"
	"strings"
)

// PodRef is the complete Kubernetes identity range supplied to the
// accelerator owner. UID is mandatory: names alone cannot prove that a
// replacement Pod has released the original allocation.
type PodRef struct {
	ClusterID     string `json:"cluster_id"`
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
	ContainerName string `json:"container_name"`
}

// ReleaseObservationInput is the immutable owner context needed by
// AcceleratorUsageService.ObserveRelease. It is deliberately separate from
// the Governance refund notification: accelerator evidence comes first, and
// only a released finding may be reported to Governance.
type ReleaseObservationInput struct {
	TenantID, RequestID, Actor                                     string
	ResourceID, CreateOperationID, OwnerService, DeleteOperationID string
	ClusterID, SealedAt                                            string
	Pods                                                           []PodRef
}

type ReleaseFinding string

const (
	ReleaseFindingReleased     ReleaseFinding = "ALLOCATION_RELEASED"
	ReleaseFindingStillPresent ReleaseFinding = "ALLOCATION_STILL_PRESENT"
	ReleaseFindingUnknown      ReleaseFinding = "RELEASE_UNKNOWN"
)

type ReleaseObservation struct {
	EvidenceID, EvidenceDigest string
	Finding                    ReleaseFinding
	ActivePodUIDs              []string
	UncertainNodeUIDs          []string
}

// ReleaseObserver is the Inference-side port for the accelerator release
// evidence check. The implementation must not turn an absent or incomplete
// Pod scope into a successful release.
type ReleaseObserver interface {
	ObserveRelease(context.Context, ReleaseObservationInput) (ReleaseObservation, error)
}

func ValidateReleaseObservationInput(in ReleaseObservationInput) error {
	for name, value := range map[string]string{
		"tenant_id": in.TenantID, "request_id": in.RequestID, "actor": in.Actor,
		"resource_id": in.ResourceID, "create_operation_id": in.CreateOperationID,
		"owner_service": in.OwnerService, "delete_operation_id": in.DeleteOperationID,
		"cluster_id": in.ClusterID, "sealed_at": in.SealedAt,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	seen := make(map[string]struct{}, len(in.Pods))
	for _, pod := range in.Pods {
		for name, value := range map[string]string{
			"pod.cluster_id": pod.ClusterID, "pod.namespace": pod.Namespace,
			"pod.name": pod.Name, "pod.uid": pod.UID, "pod.container_name": pod.ContainerName,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required", name)
			}
		}
		if pod.ClusterID != in.ClusterID {
			return fmt.Errorf("pod cluster_id %q does not match release cluster", pod.ClusterID)
		}
		if _, ok := seen[pod.UID]; ok {
			return fmt.Errorf("duplicate pod UID %q", pod.UID)
		}
		seen[pod.UID] = struct{}{}
	}
	return nil
}
