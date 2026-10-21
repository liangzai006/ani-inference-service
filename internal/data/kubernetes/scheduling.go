package kubernetes

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	volcanov1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

// applyInferenceScheduling selects Volcano for every inference workload. CPU
// workloads use Volcano's built-in default queue; GPU placement and queue must
// still come from the frozen Accelerator execution, without a CPU fallback.
func applyInferenceScheduling(spec RuntimeSpec, pod *corev1.PodSpec, container string) (map[string]string, error) {
	if pod == nil {
		return nil, fmt.Errorf("inference pod is nil")
	}
	if pod.SchedulerName != "" && pod.SchedulerName != "volcano" {
		return nil, fmt.Errorf("inference requires the volcano scheduler")
	}
	pod.SchedulerName = "volcano"
	annotations, err := applyGPUSnapshotToPod(spec, pod, container)
	if err != nil {
		return nil, err
	}
	if spec.Resources.GPU == nil {
		return map[string]string{volcanov1.QueueNameAnnotationKey: "default"}, nil
	}
	return annotations, nil
}
