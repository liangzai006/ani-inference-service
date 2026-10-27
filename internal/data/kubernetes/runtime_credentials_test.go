package kubernetes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/liangzai006/ani-inference-service/internal/biz/reconcile"
	"github.com/liangzai006/ani-inference-service/internal/biz/work"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type credentialObservationProvider struct {
	observation               bizreconcile.Observation
	ensureCalls, observeCalls int
}

func (p *credentialObservationProvider) Ensure(context.Context, bizreconcile.Desired) (bizreconcile.Observation, error) {
	p.ensureCalls++
	return p.observation, nil
}

func (p *credentialObservationProvider) observeCurrent(context.Context, DesiredRuntime) (bizreconcile.Observation, error) {
	p.observeCalls++
	return p.observation, nil
}

type credentialObservationRepository struct {
	desired bizreconcile.Desired
	saved   []bizreconcile.Observation
}

func (r *credentialObservationRepository) Current(context.Context, string, string) (bizreconcile.Desired, error) {
	return r.desired, nil
}

func (r *credentialObservationRepository) CurrentForWork(context.Context, work.Item) (bizreconcile.Desired, error) {
	return r.desired, nil
}

func (r *credentialObservationRepository) SaveObservation(_ context.Context, tenant, service string, generation int64, o bizreconcile.Observation) error {
	if tenant != r.desired.TenantID || service != r.desired.ServiceID || generation != r.desired.Generation || o.Generation != generation {
		return bizreconcile.ErrStaleGeneration
	}
	r.saved = append(r.saved, o)
	return nil
}

func (r *credentialObservationRepository) SaveObservationForWork(ctx context.Context, item work.Item, o bizreconcile.Observation) error {
	if item.LeaseToken != "credential-observation-lease" {
		return errors.New("unexpected observation lease")
	}
	return r.SaveObservation(ctx, item.TenantID, item.ServiceID, item.Generation, o)
}

func credentialObservationFixture(t *testing.T) (*RuntimeRouter, *credentialObservationProvider, *pullMaterialFake, client.Client, RuntimeSpec) {
	t.Helper()
	spec := imagePullTestSpec()
	kube := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	material := &pullMaterialFake{response: pullMaterialFixture()}
	sync := &SharedPullSecrets{Client: kube, Material: material}
	if err := sync.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	provider := &credentialObservationProvider{observation: bizreconcile.Observation{Generation: spec.Generation, RuntimePhase: "ready", ReadyReplicas: 1, ModelReady: true, ModelReadyKnown: true, InvocationHealth: "healthy"}}
	router := &RuntimeRouter{Source: routerSourceFake{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running"}}, DeploymentReconcile: provider, PullSecrets: sync}
	return router, provider, material, kube, spec
}

func TestRuntimeCredentialFailureStillPersistsCurrentObservation(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, rpcError := range []error{status.Error(codes.Unavailable, "pull-secret-sentinel"), status.Error(codes.DeadlineExceeded, "pull-secret-sentinel"), context.DeadlineExceeded} {
		for _, phase := range []string{"ready", "degraded"} {
			t.Run(rpcError.Error()+"/"+phase, func(t *testing.T) {
				router, provider, material, kube, spec := credentialObservationFixture(t)
				provider.observation.RuntimePhase = phase
				if phase == "degraded" {
					provider.observation.ReadyReplicas, provider.observation.InvocationHealth, provider.observation.Reason = 0, "unhealthy", "runtime readiness regressed"
				}
				var before corev1.Secret
				key := client.ObjectKey{Namespace: spec.Namespace, Name: spec.ImageSelection.PullSecretRef}
				if err := kube.Get(context.Background(), key, &before); err != nil {
					t.Fatal(err)
				}
				material.err = rpcError
				desired := bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation}
				repo := &credentialObservationRepository{desired: desired}
				r := bizreconcile.Reconciler{Repository: repo, Runtime: router}
				_, err := r.Execute(context.Background(), work.Item{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation, LeaseToken: "credential-observation-lease"})
				if err != nil {
					t.Fatalf("temporary credentials failure blocked observation: %v", err)
				}
				if provider.ensureCalls != 0 || provider.observeCalls != 1 || len(repo.saved) != 1 || !reflect.DeepEqual(repo.saved[0], provider.observation) {
					t.Fatalf("observation not preserved: ensure=%d observe=%d saves=%d", provider.ensureCalls, provider.observeCalls, len(repo.saved))
				}
				var after corev1.Secret
				if err := kube.Get(context.Background(), key, &after); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("read-only fallback modified the Secret")
				}
			})
		}
	}
	if strings.Contains(logs.String(), "pull-secret-sentinel") {
		t.Fatal("credential error leaked into logs")
	}
	if !strings.Contains(logs.String(), "image pull credential refresh temporarily unavailable") {
		t.Fatal("credential refresh failure was not logged")
	}
}

func TestRuntimeCredentialFailureKeepsHardErrorsAndCanceledContext(t *testing.T) {
	for _, rpcError := range []error{status.Error(codes.PermissionDenied, "denied"), status.Error(codes.NotFound, "missing"), status.Error(codes.Canceled, "canceled"), errors.New("unclassified dependency failure")} {
		t.Run(rpcError.Error(), func(t *testing.T) {
			router, provider, material, _, spec := credentialObservationFixture(t)
			material.err = rpcError
			_, err := router.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
			if err == nil || provider.ensureCalls != 0 || provider.observeCalls != 0 {
				t.Fatal("hard failure was downgraded")
			}
		})
	}
	router, provider, material, _, spec := credentialObservationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	material.err = status.Error(codes.Unavailable, "unavailable")
	if _, err := router.Ensure(ctx, bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation}); err == nil || provider.observeCalls != 0 || provider.ensureCalls != 0 {
		t.Fatal("canceled work continued observing")
	}
}

func TestRuntimeCredentialFailureStillBlocksInitialApply(t *testing.T) {
	spec := imagePullTestSpec()
	kube := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	material := &pullMaterialFake{err: status.Error(codes.Unavailable, "unavailable")}
	provider := &routerRuntimeFake{name: "deployment"}
	router := &RuntimeRouter{Source: routerSourceFake{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running"}}, Deployment: provider, PullSecrets: &SharedPullSecrets{Client: kube, Material: material}}
	op := inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation}
	if err := router.ApplyCR(context.Background(), op); err == nil {
		t.Fatal("control apply ignored credential failure")
	}
	if err := router.ApplyRuntime(context.Background(), op); err == nil {
		t.Fatal("runtime apply ignored credential failure")
	}
	if len(provider.calls) != 0 {
		t.Fatal("initial apply called provider without credentials")
	}
	var secrets corev1.SecretList
	if err := kube.List(context.Background(), &secrets); err != nil || len(secrets.Items) != 0 {
		t.Fatal("failed credential fetch wrote a Secret")
	}
}

func TestRuntimeCredentialRecoveryResumesSynchronization(t *testing.T) {
	router, provider, material, kube, spec := credentialObservationFixture(t)
	desired := bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation}
	material.err = status.Error(codes.Unavailable, "unavailable")
	if _, err := router.Ensure(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	material.err = nil
	material.response.Generation, material.response.Secret = 2, "updated-pull-secret-sentinel"
	if _, err := router.Ensure(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if provider.observeCalls != 1 || provider.ensureCalls != 1 {
		t.Fatal("provider did not resume ordinary reconciliation")
	}
	var secret corev1.Secret
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: spec.Namespace, Name: spec.ImageSelection.PullSecretRef}, &secret); err != nil {
		t.Fatal(err)
	}
	if secret.Annotations[imageCredentialGenerationAnnotation] != "2" || !strings.Contains(string(secret.Data[corev1.DockerConfigJsonKey]), "updated-pull-secret-sentinel") {
		t.Fatal("recovery did not update the credential generation")
	}
}
