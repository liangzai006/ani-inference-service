package model

import (
	"context"
	_ "embed"
	"fmt"
	"math"
	"strings"

	inference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed download_bundle.py
var downloadBundle string

// Materializer turns one immutable catalog artifact into verified runtime files.
// It reports readiness only after the actual download Job succeeds.
type Materializer struct {
	Catalog                                  *Client
	Source                                   kube.DesiredRuntimeSource
	Client                                   client.Client
	Reader                                   client.Reader
	StorageClass, Image, TenantID, Namespace string
}

func (m *Materializer) EnsureModel(ctx context.Context, op inference.OperationContext) (inference.ModelObservation, error) {
	unknown := inference.ModelObservation{}
	if m == nil || m.Client == nil || m.Source == nil || m.Catalog == nil || m.Image == "" || m.StorageClass == "" {
		return unknown, fmt.Errorf("model materializer dependencies are not configured")
	}
	if op.TenantID != m.TenantID || op.ServiceID == "" || op.TargetGeneration < 1 || op.LeaseToken == "" || m.Namespace == "" {
		return unknown, fmt.Errorf("model materializer scope mismatch")
	}
	desired, err := m.Source.CurrentRuntime(ctx, op.TenantID, op.ServiceID, op.TargetGeneration)
	if err != nil {
		return unknown, err
	}
	spec := desired.RuntimeSpec
	if spec.TenantID != op.TenantID || spec.ServiceID != op.ServiceID || spec.Generation != op.TargetGeneration || spec.Namespace != m.Namespace || spec.ArtifactProvider != "model" || !strings.HasSuffix(spec.ArtifactRef, ".tar") {
		return unknown, fmt.Errorf("model materializer requires matching model scope and a tar artifact")
	}
	snapshot, err := m.Catalog.GetReadyVersion(ctx, op.TenantID, spec.ModelVersionID)
	if err != nil {
		return unknown, err
	}
	if snapshot.ArtifactRef != spec.ArtifactRef || snapshot.ArtifactSHA256 != spec.ArtifactSHA256 {
		return unknown, fmt.Errorf("Model artifact differs from immutable Inference snapshot")
	}
	reader := m.Reader
	if reader == nil {
		reader = m.Client
	}
	name := kube.ModelClaimName(spec)
	labels := map[string]string{"ani.kubercloud.com/tenant-id": op.TenantID, "ani.kubercloud.com/service-id": op.ServiceID, "ani.kubercloud.com/generation": fmt.Sprint(op.TargetGeneration)}
	annotations := map[string]string{"ani.kubercloud.com/artifact-sha256": spec.ArtifactSHA256, "ani.kubercloud.com/model-version-id": spec.ModelVersionID}
	meta := func(n string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: n, Namespace: m.Namespace, Labels: labels, Annotations: annotations}
	}
	owned := func(obj client.Object) error {
		for k, v := range labels {
			if obj.GetLabels()[k] != v {
				return fmt.Errorf("materialization object ownership mismatch")
			}
		}
		for k, v := range annotations {
			if obj.GetAnnotations()[k] != v {
				return fmt.Errorf("materialization artifact identity mismatch")
			}
		}
		if obj.GetDeletionTimestamp() != nil {
			return fmt.Errorf("materialization object is terminating")
		}
		return nil
	}
	job := &batchv1.Job{}
	err = reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name + "-fetch"}, job)
	jobMissing := apierrors.IsNotFound(err)
	if err != nil && !jobMissing {
		return unknown, err
	}
	if !jobMissing {
		if err = owned(job); err != nil {
			return unknown, err
		}
		containers := job.Spec.Template.Spec.Containers
		if len(containers) != 1 || containers[0].Image != m.Image || len(containers[0].Args) != 2 || containers[0].Args[1] != downloadBundle {
			return unknown, fmt.Errorf("materialization Job implementation mismatch")
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			if condition.Type == batchv1.JobComplete {
				verifiedClaim := &corev1.PersistentVolumeClaim{}
				if err = reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name}, verifiedClaim); err != nil {
					return unknown, err
				}
				if err = owned(verifiedClaim); err != nil {
					return unknown, err
				}
				if verifiedClaim.Status.Phase != corev1.ClaimBound || string(verifiedClaim.UID) != job.Annotations["ani.kubercloud.com/model-claim-uid"] {
					return unknown, fmt.Errorf("verified model PVC is unavailable or replaced")
				}
				return inference.ModelObservation{Known: true, Ready: true, Reason: "model archive downloaded and checksum verified by Job"}, nil
			}
			if condition.Type == batchv1.JobFailed {
				return inference.ModelObservation{Known: true, Reason: "model download Job failed; inspect sanitized container logs"}, nil
			}
		}
	}
	claim := &corev1.PersistentVolumeClaim{}
	err = reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name}, claim)
	if apierrors.IsNotFound(err) {
		claim = &corev1.PersistentVolumeClaim{ObjectMeta: meta(name), Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: ptr.To(m.StorageClass), AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(pvcSize(snapshot.ArtifactSizeBytes))}}}}
		if err = m.Client.Create(ctx, claim); err != nil {
			return unknown, err
		}
	} else if err != nil {
		return unknown, err
	} else if err = owned(claim); err != nil {
		return unknown, err
	}
	download, err := m.Catalog.GetArtifactDownloadURL(ctx, op.TenantID, spec.ModelVersionID, "inference-materializer")
	if err != nil {
		return unknown, err
	}
	if download.StoragePath != spec.ArtifactRef || !strings.HasPrefix(download.URL, "https:") {
		return unknown, fmt.Errorf("model download identity or transport mismatch")
	}
	secret := &corev1.Secret{}
	err = reader.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: name + "-download"}, secret)
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: meta(name + "-download"), Data: map[string][]byte{"url": []byte(download.URL)}}
		if err = m.Client.Create(ctx, secret); err != nil {
			return unknown, fmt.Errorf("create model download credential: %T", err)
		}
	} else if err != nil {
		return unknown, fmt.Errorf("read model download credential: %T", err)
	} else {
		if err = owned(secret); err != nil {
			return unknown, err
		}
		secret.Data = map[string][]byte{"url": []byte(download.URL)}
		if err = m.Client.Update(ctx, secret); err != nil {
			return unknown, fmt.Errorf("refresh model download credential: %T", err)
		}
	}
	if jobMissing {
		annotations["ani.kubercloud.com/model-claim-uid"] = string(claim.UID)
		job = &batchv1.Job{ObjectMeta: meta(name + "-fetch"), Spec: batchv1.JobSpec{BackoffLimit: ptr.To(int32(2)), ActiveDeadlineSeconds: ptr.To(int64(600)), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), Volumes: []corev1.Volume{{Name: "model", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}}}, Containers: []corev1.Container{{Name: "download", Image: m.Image, Command: []string{"python3"}, Args: []string{"-c", downloadBundle}, VolumeMounts: []corev1.VolumeMount{{Name: "model", MountPath: "/model"}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}}, Env: []corev1.EnvVar{{Name: "MODEL_SHA256", Value: spec.ArtifactSHA256}, {Name: "MODEL_DOWNLOAD_URL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}, Key: "url"}}}}}}}}}}
		if err = m.Client.Create(ctx, job); err != nil {
			return unknown, err
		}
	}
	return inference.ModelObservation{Reason: "model download Job is pending"}, nil
}

func pvcSize(bytes int64) string {
	if bytes <= 0 {
		return "1Gi"
	}
	const overhead = 2
	gi := int64(1 << 30)
	need := int64(math.Ceil(float64(bytes*overhead) / float64(gi)))
	if need < 1 {
		need = 1
	}
	return fmt.Sprintf("%dGi", need)
}
