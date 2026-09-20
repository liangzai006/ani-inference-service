package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	inference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
	"google.golang.org/protobuf/types/known/timestamppb"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"time"
)

type materializerSource struct{ spec kube.DesiredRuntime }

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
	api := &modelAPIFake{version: v, download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "https://storage/private?secret=redacted", StoragePath: v.StoragePath, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}
	spec := kube.RuntimeSpec{TenantID: "tenant", ServiceID: "service", Namespace: "ani-model-inference-test", Generation: 1, ModelVersionID: v.Id, ArtifactProvider: "model", ArtifactRef: v.StoragePath, ArtifactSHA256: v.ChecksumSha256, RuntimeMode: "deployment"}
	m := &Materializer{Catalog: NewClient(api), Source: materializerSource{kube.DesiredRuntime{RuntimeSpec: spec}}, Client: kc, Reader: kc, Namespace: spec.Namespace, StorageClass: "cephfs", Image: "image@sha256:fixed"}
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
