package kubernetes

import (
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ModelClaimName(spec RuntimeSpec) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", spec.TenantID, spec.ServiceID, spec.Generation)))
	return fmt.Sprintf("ani-model-%x", sum[:12])
}

func mountModel(pod *corev1.PodSpec, spec RuntimeSpec) error {
	if spec.ModelClaim == "" {
		return nil
	}
	if spec.ArtifactProvider != "model" || spec.ContainerPort <= 0 {
		return fmt.Errorf("model PVC requires Model provider and an explicit endpoint")
	}
	pod.AutomountServiceAccountToken = boolPtr(false)
	pod.Volumes = []corev1.Volume{
		{Name: "model", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: spec.ModelClaim, ReadOnly: true}}},
		{Name: "shm", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: quantityPtr("512Mi")}}},
	}
	c := &pod.Containers[0]
	c.VolumeMounts = []corev1.VolumeMount{{Name: "model", MountPath: "/models", SubPath: "data", ReadOnly: true}, {Name: "shm", MountPath: "/dev/shm"}}
	if spec.EngineRuntime == "vllm" {
		c.Env = append(c.Env, corev1.EnvVar{Name: "HF_HUB_OFFLINE", Value: "1"}, corev1.EnvVar{Name: "TRANSFORMERS_OFFLINE", Value: "1"})
	}
	handler := corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(spec.ContainerPort)}}
	c.StartupProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 120}
	c.ReadinessProbe = &corev1.Probe{ProbeHandler: handler, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 3}
	return nil
}

func boolPtr(v bool) *bool                    { return &v }
func quantityPtr(v string) *resource.Quantity { q := resource.MustParse(v); return &q }
