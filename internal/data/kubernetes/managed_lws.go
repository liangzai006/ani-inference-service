package kubernetes

import (
	"fmt"

	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

// ProjectManagedGPULeaderWorkerSet is an explicit boundary for the currently
// unsupported GPU LWS path. GPU execution is rendered only by the regular
// KServe Deployment path; callers must handle this error instead of silently
// downgrading to an unplanned LWS workload.
func ProjectManagedGPULeaderWorkerSet(spec RuntimeSpec, input *lwsv1.LeaderWorkerSet) (*lwsv1.LeaderWorkerSet, error) {
	if !spec.ManagedGPU || spec.RuntimeMode != "leader_worker_set" {
		return nil, fmt.Errorf("accepted Governance GPU LWS snapshot is required")
	}
	// ACC execution is currently supported on the regular KServe Deployment
	// path only. Never silently project a frozen GPU execution onto an LWS.
	return nil, fmt.Errorf("GPU LeaderWorkerSet is explicitly unsupported")
}
