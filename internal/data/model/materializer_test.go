package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	inference "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	kube "github.com/liangzai006/ani-inference-service/internal/data/kubernetes"
	"google.golang.org/protobuf/types/known/timestamppb"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"time"
)

type materializerSource struct{ spec kube.DesiredRuntime }

type namespaceOnlyReader struct {
	client.Reader
	namespace string
}

func (r namespaceOnlyReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	options := &client.ListOptions{}
	for _, option := range opts {
		option.ApplyToList(options)
	}
	if options.Namespace != r.namespace {
		return fmt.Errorf("list must be namespace-scoped to %q, got %q", r.namespace, options.Namespace)
	}
	return r.Reader.List(ctx, list, opts...)
}

func (s materializerSource) CurrentRuntime(context.Context, string, string, int64) (kube.DesiredRuntime, error) {
	return s.spec, nil
}

func TestMaterializerRequiresVerifiedJobBeforeReady(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	kc := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&batchv1.Job{}).Build()
	v := readyVersion()
	v.StoragePath = "tenant/model.tar"
	api := &modelAPIFake{version: v, download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "http://storage/private?secret=redacted", StoragePath: v.StoragePath, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}
	spec := kube.RuntimeSpec{TenantID: "tenant", ServiceID: "service", Namespace: "ani-model-inference-test", Generation: 1, ModelVersionID: v.Id, ArtifactProvider: "model", ArtifactRef: v.StoragePath, ArtifactSHA256: v.ChecksumSha256, RuntimeMode: "deployment"}
	m := &Materializer{Catalog: NewClient(api), Source: materializerSource{kube.DesiredRuntime{RuntimeSpec: spec}}, Client: kc, Reader: kc, Namespace: spec.Namespace, StorageClass: "cephfs", Image: "image@sha256:fixed", ServiceAccountName: "ani-inference-runtime", DownloadCAConfigMap: "model-ca", DownloadCAKey: "bundle.pem"}
	op := inference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, ID: "distinct-operation-id", LeaseToken: "lease"}
	observation, err := m.EnsureModel(ctx, op)
	if err != nil || observation.Ready {
		t.Fatalf("before Job completion: %+v %v", observation, err)
	}
	if api.request.ModelVersionId != v.Id {
		t.Fatal("used wrong model version ID")
	}
	jobs := &batchv1.JobList{}
	if err = kc.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatal("expected one materialization job", err)
	}
	job := &jobs.Items[0]
	var caPath string
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "SSL_CERT_FILE" {
			caPath = env.Value
		}
	}
	if caPath == "" {
		t.Fatal("download Job did not receive the configured TLS CA bundle")
	}
	var caMount *corev1.VolumeMount
	for i := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		mount := &job.Spec.Template.Spec.Containers[0].VolumeMounts[i]
		if caPath == mount.MountPath+"/ca-certificates.crt" && mount.ReadOnly {
			caMount = mount
		}
	}
	if caMount == nil {
		t.Fatal("download CA bundle must be mounted read-only")
	}
	var caVolume *corev1.ConfigMapVolumeSource
	for _, volume := range job.Spec.Template.Spec.Volumes {
		if volume.Name == caMount.Name {
			caVolume = volume.ConfigMap
		}
	}
	if caVolume == nil || caVolume.Name != "model-ca" || len(caVolume.Items) != 1 || caVolume.Items[0].Key != "bundle.pem" || caVolume.Items[0].Path != "ca-certificates.crt" {
		t.Fatalf("download Job CA volume = %+v", caVolume)
	}
	if job.Spec.Template.Spec.ServiceAccountName != "ani-inference-runtime" {
		t.Fatalf("materialization Job service account = %q, want ani-inference-runtime", job.Spec.Template.Spec.ServiceAccountName)
	}
	var maxBytes string
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "MODEL_MAX_BYTES" {
			maxBytes = env.Value
		}
	}
	if maxBytes != "3221225472" {
		t.Fatalf("materializer max size=%q, want artifact size", maxBytes)
	}
	if strings.Contains(strings.Join(job.Spec.Template.Spec.Containers[0].Args, " "), "private?secret") {
		t.Fatal("signed URL leaked in Job args")
	}
	if observation, err = m.EnsureModel(ctx, op); err != nil || observation.Ready {
		t.Fatal("pending Job became ready", err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err = kc.Get(ctx, client.ObjectKey{Namespace: spec.Namespace, Name: kube.ModelClaimName(spec)}, claim); err != nil {
		t.Fatal(err)
	}
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteMany {
		t.Fatalf("shared storage access modes = %v, want ReadWriteMany", claim.Spec.AccessModes)
	}
	claim.Status.Phase = corev1.ClaimBound
	if err = kc.Status().Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	if err = kc.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	if observation, err = m.EnsureModel(ctx, op); err != nil || !observation.Known || !observation.Ready {
		t.Fatal("verified complete Job not ready", err)
	}
	api.err = errors.New("model version deleted after admission")
	if observation, err = m.EnsureModel(ctx, op); err == nil || observation.Ready {
		t.Fatal("completed Job bypassed Model revalidation", observation, err)
	}
	api.err = nil
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}
	if err = kc.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	if observation, err = m.EnsureModel(ctx, op); err == nil || observation.Ready {
		t.Fatal("failed Job did not trigger a retry reset", observation, err)
	}
	secrets := &corev1.SecretList{}
	if err = kc.List(ctx, secrets); err != nil || len(secrets.Items) != 0 {
		t.Fatal("failed materialization left a download Secret", err)
	}
	if observation, err = m.EnsureModel(ctx, op); err != nil || observation.Ready {
		t.Fatal("failed materialization was not recreated", observation, err)
	}
	if err = kc.List(ctx, jobs); err != nil || len(jobs.Items) != 1 {
		t.Fatal("expected a replacement materialization Job", err)
	}
	op.TenantID = "attacker"
	if _, err = m.EnsureModel(ctx, op); err == nil {
		t.Fatal("cross tenant accepted")
	}
	_ = client.ObjectKey{}
}

func TestMaterializerCleanupDeletesOnlyOwnedResources(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	namespace := "ani-model-inference-test"
	owned := map[string]string{
		"ani.kubercloud.com/tenant-id":  "tenant",
		"ani.kubercloud.com/service-id": "service",
	}
	other := map[string]string{
		"ani.kubercloud.com/tenant-id":  "other-tenant",
		"ani.kubercloud.com/service-id": "service",
	}
	ownedGeneration := func(generation string) map[string]string {
		labels := map[string]string{}
		for key, value := range owned {
			labels[key] = value
		}
		labels["ani.kubercloud.com/generation"] = generation
		labels[materializationComponentLabel] = materializationComponentValue
		return labels
	}
	legacyLabels := map[string]string{}
	for key, value := range owned {
		legacyLabels[key] = value
	}
	legacyLabels["ani.kubercloud.com/generation"] = "3"
	legacyAnnotations := map[string]string{"ani.kubercloud.com/model-version-id": "legacy-version"}
	objects := []client.Object{
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned-fetch-1", Namespace: namespace, Labels: ownedGeneration("1")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned-fetch-2", Namespace: namespace, Labels: ownedGeneration("2")}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owned-pod", Namespace: namespace, Labels: ownedGeneration("1")}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "owned-claim", Namespace: namespace, Labels: ownedGeneration("2")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "owned-download", Namespace: namespace, Labels: ownedGeneration("1")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "legacy-fetch", Namespace: namespace, Labels: legacyLabels, Annotations: legacyAnnotations}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "legacy-pod", Namespace: namespace, Labels: map[string]string{"ani.kubercloud.com/tenant-id": "tenant", "ani.kubercloud.com/service-id": "service", "job-name": "legacy-fetch"}}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "legacy-claim", Namespace: namespace, Labels: legacyLabels, Annotations: legacyAnnotations}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "legacy-download", Namespace: namespace, Labels: legacyLabels, Annotations: legacyAnnotations}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "runtime-pod", Namespace: namespace, Labels: owned}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "other-fetch", Namespace: namespace, Labels: other}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: namespace, Labels: other}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "other-claim", Namespace: namespace, Labels: other}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-download", Namespace: namespace, Labels: other}},
	}
	kc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	m := &Materializer{Client: kc, Reader: namespaceOnlyReader{Reader: kc, namespace: namespace}, Namespace: namespace}
	op := inference.OperationContext{TenantID: "tenant", ServiceID: "service", TargetGeneration: 2, LeaseToken: "lease"}
	if err := m.CleanupModel(ctx, op); err != nil {
		t.Fatal(err)
	}
	for _, object := range []client.Object{
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned-fetch-1", Namespace: namespace}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "owned-fetch-2", Namespace: namespace}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "owned-pod", Namespace: namespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "owned-claim", Namespace: namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "owned-download", Namespace: namespace}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "legacy-fetch", Namespace: namespace}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "legacy-pod", Namespace: namespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "legacy-claim", Namespace: namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "legacy-download", Namespace: namespace}},
	} {
		if err := kc.Get(ctx, client.ObjectKeyFromObject(object), object); err == nil || !apierrors.IsNotFound(err) {
			t.Fatalf("owned %T %s still exists: %v", object, object.GetName(), err)
		}
	}
	if err := kc.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "runtime-pod"}, &corev1.Pod{}); err != nil {
		t.Fatalf("runtime pod was deleted: %v", err)
	}
	for _, object := range []client.Object{
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "other-fetch", Namespace: namespace}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other-pod", Namespace: namespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "other-claim", Namespace: namespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other-download", Namespace: namespace}},
	} {
		if err := kc.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("unowned %T %s was deleted: %v", object, object.GetName(), err)
		}
	}
}

func TestMaterializerUsesReadWriteOnceForLocalPath(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	kc := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&batchv1.Job{}).Build()
	v := readyVersion()
	v.StoragePath = "tenant/model.tar"
	api := &modelAPIFake{version: v, download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "http://storage/private", StoragePath: v.StoragePath, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}
	spec := kube.RuntimeSpec{TenantID: "tenant", ServiceID: "local-path-service", Namespace: "ani-model-inference-test", Generation: 1, ModelVersionID: v.Id, ArtifactProvider: "model", ArtifactRef: v.StoragePath, ArtifactSHA256: v.ChecksumSha256, RuntimeMode: "deployment"}
	m := &Materializer{Catalog: NewClient(api), Source: materializerSource{kube.DesiredRuntime{RuntimeSpec: spec}}, Client: kc, Reader: kc, Namespace: spec.Namespace, StorageClass: "local-path", Image: "image@sha256:fixed"}
	op := inference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, ID: "local-path-operation", LeaseToken: "lease"}
	if _, err := m.EnsureModel(ctx, op); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := kc.Get(ctx, client.ObjectKey{Namespace: spec.Namespace, Name: kube.ModelClaimName(spec)}, claim); err != nil {
		t.Fatal(err)
	}
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("local-path access modes = %v, want ReadWriteOnce", claim.Spec.AccessModes)
	}
}

func TestMaterializerUsesClusterDefaultStorageClass(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	kc := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&batchv1.Job{}).Build()
	v := readyVersion()
	v.StoragePath = "tenant/model.tar"
	api := &modelAPIFake{version: v, download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "http://storage/private", StoragePath: v.StoragePath, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}
	spec := kube.RuntimeSpec{TenantID: "tenant", ServiceID: "default-storage-service", Namespace: "ani-model-inference-test", Generation: 1, ModelVersionID: v.Id, ArtifactProvider: "model", ArtifactRef: v.StoragePath, ArtifactSHA256: v.ChecksumSha256, RuntimeMode: "deployment"}
	m := &Materializer{Catalog: NewClient(api), Source: materializerSource{kube.DesiredRuntime{RuntimeSpec: spec}}, Client: kc, Reader: kc, Namespace: spec.Namespace, StorageClass: "", Image: "image@sha256:fixed"}
	op := inference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, ID: "default-storage-operation", LeaseToken: "lease"}
	if _, err := m.EnsureModel(ctx, op); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{}
	if err := kc.Get(ctx, client.ObjectKey{Namespace: spec.Namespace, Name: kube.ModelClaimName(spec)}, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Spec.StorageClassName != nil {
		t.Fatalf("storage class = %q, want nil for cluster default", *claim.Spec.StorageClassName)
	}
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("cluster default access modes = %v, want ReadWriteOnce", claim.Spec.AccessModes)
	}
}

func TestMaterializerRejectsUnknownArtifactSize(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	kc := fake.NewClientBuilder().WithScheme(scheme).Build()
	v := readyVersion()
	v.StoragePath = "tenant/model.tar"
	v.SizeBytes = 0
	api := &modelAPIFake{version: v, download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "http://storage/private", StoragePath: v.StoragePath, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}
	spec := kube.RuntimeSpec{TenantID: "tenant", ServiceID: "service", Namespace: "ani-model-inference-test", Generation: 1, ModelVersionID: v.Id, ArtifactProvider: "model", ArtifactRef: v.StoragePath, ArtifactSHA256: v.ChecksumSha256, RuntimeMode: "deployment"}
	m := &Materializer{Catalog: NewClient(api), Source: materializerSource{kube.DesiredRuntime{RuntimeSpec: spec}}, Client: kc, Reader: kc, Namespace: spec.Namespace, StorageClass: "cephfs", Image: "image@sha256:fixed"}
	op := inference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, ID: "unknown-size-operation", LeaseToken: "lease"}
	if _, err := m.EnsureModel(ctx, op); err == nil || !strings.Contains(err.Error(), "artifact size must be positive") {
		t.Fatalf("EnsureModel() error = %v, want positive artifact size error", err)
	}
}
