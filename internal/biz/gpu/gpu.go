// Package gpu contains the transport-neutral accelerator contract used by
// Inference. It intentionally has no protobuf or Kubernetes dependency.
package gpu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

type ResolveInput struct {
	TenantID  string
	RequestID string
	Actor     string
	Request   *Request
}

type Resolver interface {
	Resolve(context.Context, ResolveInput) (*Snapshot, error)
}

type Request struct {
	ClusterID         string `json:"cluster_id"`
	PoolID            string `json:"pool_id"`
	ProfileID         string `json:"profile_id"`
	ProfileVersion    uint64 `json:"profile_version"`
	Replicas          uint32 `json:"replicas"`
	DevicesPerReplica uint32 `json:"devices_per_replica"`
	ContainerName     string `json:"container_name"`
}

// Execution is the exact ACC scheduler projection. Inference freezes and
// renders these values byte-for-byte; it never derives provider settings.
type Execution struct {
	SchedulerName    string            `json:"scheduler_name"`
	QueueName        string            `json:"queue_name"`
	RuntimeClassName string            `json:"runtime_class_name"`
	NodeSelector     map[string]string `json:"node_selector,omitempty"`
	PodAnnotations   map[string]string `json:"pod_annotations,omitempty"`
	Resources        map[string]string `json:"resources,omitempty"`
	ExecutionDigest  string            `json:"execution_digest"`
}

// Display is the read-only accelerator selection shown with the service.
type Display struct {
	ProfileID      string `json:"profile_id"`
	ProfileVersion uint64 `json:"profile_version"`
	ProfileName    string `json:"profile_name"`
	ModelKey       string `json:"model_key"`
	Mode           int32  `json:"mode"`
}

type QuotaItem struct {
	QuotaCode string `json:"quota_code"`
	Units     int64  `json:"units"`
}

// Snapshot is the immutable result of ACC ResolveGpuRequest. Cluster and
// tenant/operation identity remain in Inference-owned records around it.
type Snapshot struct {
	Request         *Request    `json:"request"`
	Execution       *Execution  `json:"execution"`
	Display         *Display    `json:"display"`
	QuotaItems      []QuotaItem `json:"quota_items,omitempty"`
	ExecutionDigest string      `json:"execution_digest"`
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidateRequest(r *Request) error {
	if r == nil || !uuidPattern.MatchString(r.ClusterID) || !uuidPattern.MatchString(r.PoolID) || !uuidPattern.MatchString(r.ProfileID) || r.ProfileVersion == 0 {
		return fmt.Errorf("invalid GPU request")
	}
	if r.Replicas < 1 || r.Replicas > 16 || r.DevicesPerReplica < 1 || len(r.ContainerName) == 0 || len(r.ContainerName) > 63 || !namePattern.MatchString(r.ContainerName) {
		return fmt.Errorf("unsupported GPU request shape")
	}
	return nil
}

func ValidateTopology(r *Request, replicas int32, mode string, workers int32) error {
	if err := ValidateRequest(r); err != nil {
		return err
	}
	if replicas < 1 {
		return fmt.Errorf("replicas must be positive")
	}
	expected, err := TotalPods(replicas, mode, workers)
	if err != nil {
		return err
	}
	if r.Replicas != uint32(expected) {
		return fmt.Errorf("GPU replicas must match GPU-bearing pod count %d", expected)
	}
	switch mode {
	case "", "deployment":
		if workers > 1 || workers < 0 {
			return fmt.Errorf("worker_replicas is only valid for leader_worker_set")
		}
		if r.ContainerName != "kserve-container" {
			return fmt.Errorf("deployment GPU container must be kserve-container")
		}
	case "leader_worker_set":
		if workers < 1 {
			return fmt.Errorf("leader_worker_set requires workers")
		}
		if r.ContainerName != "main" {
			return fmt.Errorf("leader_worker_set GPU container must be main")
		}
	default:
		return fmt.Errorf("unsupported runtime mode %q", mode)
	}
	return nil
}

func TotalPods(replicas int32, mode string, workers int32) (int32, error) {
	if replicas < 1 {
		return 0, fmt.Errorf("replicas must be positive")
	}
	switch mode {
	case "", "deployment":
		if workers > 1 || workers < 0 {
			return 0, fmt.Errorf("worker_replicas is only valid for leader_worker_set")
		}
		return replicas, nil
	case "leader_worker_set":
		if workers < 1 {
			return 0, fmt.Errorf("leader_worker_set requires workers")
		}
		total := int64(replicas) * (int64(workers) + 1)
		if total > math.MaxInt32 {
			return 0, fmt.Errorf("GPU topology overflows pod count")
		}
		return int32(total), nil
	default:
		return 0, fmt.Errorf("unsupported runtime mode %q", mode)
	}
}

func ValidateSnapshot(s *Snapshot, request *Request) error {
	if s == nil || request == nil || s.Request == nil || *s.Request != *request || s.Execution == nil || s.Display == nil {
		return fmt.Errorf("invalid GPU execution snapshot")
	}
	if err := ValidateRequest(request); err != nil {
		return err
	}
	if err := ValidateExecution(s.Execution); err != nil {
		return err
	}
	if !uuidPattern.MatchString(s.Display.ProfileID) || s.Display.ProfileID != request.ProfileID || s.Display.ProfileVersion == 0 || strings.TrimSpace(s.Display.ProfileName) == "" || strings.TrimSpace(s.Display.ModelKey) == "" {
		return fmt.Errorf("invalid GPU display selection")
	}
	if s.Display.ProfileVersion != request.ProfileVersion || (s.Display.Mode != 1 && s.Display.Mode != 2) {
		return fmt.Errorf("GPU display does not match request")
	}
	if s.ExecutionDigest == "" {
		s.ExecutionDigest = s.Execution.ExecutionDigest
	}
	if s.ExecutionDigest == "" || s.Execution.ExecutionDigest == "" || s.ExecutionDigest != s.Execution.ExecutionDigest {
		return fmt.Errorf("GPU execution digest is required")
	}
	return nil
}

func ValidateExecution(e *Execution) error {
	if e == nil || !digestPattern.MatchString(strings.TrimSpace(e.ExecutionDigest)) {
		return fmt.Errorf("GPU execution digest is required")
	}
	for name, values := range map[string]map[string]string{"node_selector": e.NodeSelector, "pod_annotations": e.PodAnnotations, "resources": e.Resources} {
		for k, v := range values {
			if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
				return fmt.Errorf("empty GPU execution %s key/value", name)
			}
		}
	}
	return nil
}

// SnapshotDigest is an Inference-owned stable digest for persistence and
// replay. The ACC execution digest remains the authoritative provider digest.
func SnapshotDigest(s *Snapshot) (string, error) {
	if s == nil || s.Request == nil || s.Execution == nil || s.Display == nil {
		return "", fmt.Errorf("invalid GPU execution snapshot")
	}
	canonical := struct {
		Request    *Request    `json:"request"`
		Execution  *Execution  `json:"execution"`
		Display    *Display    `json:"display"`
		QuotaItems []QuotaItem `json:"quota_items,omitempty"`
	}{s.Request, s.Execution, s.Display, sortedQuota(s.QuotaItems)}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("ani-inference-gpu-execution-v1\n"), b...))
	return hex.EncodeToString(sum[:]), nil
}

func sortedQuota(items []QuotaItem) []QuotaItem {
	out := append([]QuotaItem(nil), items...)
	sort.Slice(out, func(i, j int) bool { return out[i].QuotaCode < out[j].QuotaCode })
	return out
}
