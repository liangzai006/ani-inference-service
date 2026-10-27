package gpu

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PodRef is the complete Kubernetes identity range recorded while the
// managed runtime existed. UID is mandatory: names alone cannot distinguish
// a replacement Pod from the original allocation holder.
type PodRef struct {
	ClusterID     string `json:"cluster_id"`
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	UID           string `json:"uid"`
	ContainerName string `json:"container_name"`
}

// ReleaseProofInput is the Inference-owned fact that the managed runtime is
// gone. SealedAt is written only when the runner (or DELETE-first close)
// confirmed runtime absence; the Pod scope is whatever was ever observed.
// No external owner is consulted: Inference deleted the runtime and is the
// only party that has to confirm it before Governance refunds.
type ReleaseProofInput struct {
	TenantID, ResourceID, CreateOperationID, DeleteOperationID string
	SealedAt                                                   string
	Pods                                                       []PodRef
}

// ReleaseProof is the durable evidence persisted with the release report.
// It is deterministic so a retried release reproduces the same row.
type ReleaseProof struct {
	EvidenceID, EvidenceDigest string
}

func ValidateReleaseProofInput(in ReleaseProofInput) error {
	for name, value := range map[string]string{
		"tenant_id": in.TenantID, "resource_id": in.ResourceID,
		"create_operation_id": in.CreateOperationID, "delete_operation_id": in.DeleteOperationID,
		"sealed_at": in.SealedAt,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, in.SealedAt); err != nil {
		return fmt.Errorf("sealed_at must be RFC3339Nano: %w", err)
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
		if _, ok := seen[pod.UID]; ok {
			return fmt.Errorf("duplicate pod UID %q", pod.UID)
		}
		seen[pod.UID] = struct{}{}
	}
	return nil
}

// NewReleaseProof derives the evidence identity from the delete operation
// and the digest from the sealed absence fact plus the UID-sorted Pod scope.
func NewReleaseProof(in ReleaseProofInput) (ReleaseProof, error) {
	if err := ValidateReleaseProofInput(in); err != nil {
		return ReleaseProof{}, err
	}
	pods := append([]PodRef(nil), in.Pods...)
	sort.Slice(pods, func(i, j int) bool { return pods[i].UID < pods[j].UID })
	in.Pods = pods
	payload, err := json.Marshal(in)
	if err != nil {
		return ReleaseProof{}, err
	}
	sum := sha256.Sum256(payload)
	return ReleaseProof{
		EvidenceID:     uuid.NewSHA1(uuid.NameSpaceURL, []byte("ani-inference/managed-gpu-release-proof/"+in.DeleteOperationID)).String(),
		EvidenceDigest: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}
