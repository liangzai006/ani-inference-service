package kubernetes

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestApplyNonRootSecurity(t *testing.T) {
	pod := &corev1.PodSpec{
		ServiceAccountName: "ani-runtime",
		Containers: []corev1.Container{{
			Name: "engine", Command: []string{"sh"}, Args: []string{"-c", "vllm serve /models"},
		}},
	}

	ApplyNonRootSecurity(pod)

	if pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 {
		t.Fatalf("RunAsUser = %#v, want 65534", pod.SecurityContext)
	}
	if pod.SecurityContext.RunAsGroup == nil || *pod.SecurityContext.RunAsGroup != 65534 ||
		pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 65534 {
		t.Fatalf("pod security context = %#v, want non-root 65534", pod.SecurityContext)
	}
	if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccomp profile = %#v, want RuntimeDefault", pod.SecurityContext.SeccompProfile)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automount token = %v, want false", pod.AutomountServiceAccountToken)
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("container security context = %#v, want no privilege escalation", container.SecurityContext)
	}
	if len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Fatalf("capability drop = %#v, want ALL", container.SecurityContext.Capabilities.Drop)
	}
	if string(container.Command[0]) != "sh" || container.Args[1] != "vllm serve /models" {
		t.Fatalf("command changed: %v %v", container.Command, container.Args)
	}
}
