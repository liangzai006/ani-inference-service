package kubernetes

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// 65534 is the image-independent nobody user present in the runtime images.
// Using it instead of an arbitrary numeric UID keeps Python runtimes that call
// getpass.getuser() functional while still enforcing non-root execution.
const inferenceRuntimeUID int64 = 65534

// ApplyNonRootSecurity applies the runtime security contract to a workload pod.
// It intentionally leaves commands, resources, mounts, scheduling fields and
// service account names untouched.
func ApplyNonRootSecurity(pod *corev1.PodSpec) {
	if pod == nil {
		return
	}
	uid := inferenceRuntimeUID
	pod.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser:    ptr.To(uid),
		RunAsGroup:   ptr.To(uid),
		RunAsNonRoot: ptr.To(true),
		FSGroup:      ptr.To(uid),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	pod.AutomountServiceAccountToken = ptr.To(false)
	for index := range pod.Containers {
		pod.Containers[index].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{corev1.Capability("ALL")},
			},
		}
	}
}
