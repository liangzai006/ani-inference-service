package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/liangzai006/ani-inference-service/internal/biz/reconcile"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type pullMaterialFake struct {
	response *imagev1.GetTenantPullMaterialResponse
	err      error
	calls    int
}

func (m *pullMaterialFake) PullMaterial(_ context.Context, tenant string) (*imagev1.GetTenantPullMaterialResponse, error) {
	m.calls++
	return m.response, m.err
}

func imagePullTestSpec() RuntimeSpec {
	spec := RuntimeSpec{TenantID: "11111111-1111-4111-8111-111111111111", ServiceID: "22222222-2222-4222-8222-222222222222", Name: "image-test", Namespace: "models", Image: "registry.test/platform/engine@sha256:" + strings.Repeat("a", 64), Generation: 1, Replicas: 1, WorkerReplicas: 1}
	spec.ImageSelection = &inferencebiz.ImageSelection{ImageID: "img_" + strings.Repeat("a", 32), Scope: "platform", OS: "linux", Architecture: "amd64", RegistryAuthority: "registry.test", PullSecretRef: inferencebiz.PullSecretName(spec.TenantID, spec.Namespace, "registry.test")}
	return spec
}

func pullMaterialFixture() *imagev1.GetTenantPullMaterialResponse {
	return &imagev1.GetTenantPullMaterialResponse{TenantId: imagePullTestSpec().TenantID, SpaceId: "space-a", RegistryAuthority: "registry.test", Username: "robot$pull-a", Secret: "pull-secret-sentinel", Generation: 1, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}
}

func TestSharedPullSecretScopeOwnershipAndGeneration(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spec := imagePullTestSpec()
	material := &pullMaterialFake{response: pullMaterialFixture()}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	sync := &SharedPullSecrets{Client: kube, Material: material}
	if err := sync.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: spec.Namespace, Name: spec.ImageSelection.PullSecretRef}
	var original corev1.Secret
	if err := kube.Get(ctx, key, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.OwnerReferences) != 0 || original.Type != corev1.SecretTypeDockerConfigJson || original.Labels[tenantIDLabel] != spec.TenantID {
		t.Fatal("shared Secret ownership/type incorrect")
	}
	if !strings.Contains(string(original.Data[corev1.DockerConfigJsonKey]), "pull-secret-sentinel") {
		t.Fatal("credential missing from Secret")
	}
	otherService := spec
	otherService.ServiceID = "33333333-3333-4333-8333-333333333333"
	if err := sync.Ensure(ctx, otherService); err != nil {
		t.Fatal(err)
	}
	var replayed corev1.Secret
	if err := kube.Get(ctx, key, &replayed); err != nil {
		t.Fatal(err)
	}
	if original.ResourceVersion != replayed.ResourceVersion {
		t.Fatal("same credential generation rewrote shared Secret")
	}
	material.response.Generation = 2
	material.response.Secret = "new-secret-sentinel"
	if err := sync.Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	var updated corev1.Secret
	if err := kube.Get(ctx, key, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Annotations[imageCredentialGenerationAnnotation] != "2" || updated.ResourceVersion == original.ResourceVersion || !strings.Contains(string(updated.Data[corev1.DockerConfigJsonKey]), "new-secret-sentinel") {
		t.Fatal("credential generation was not synchronized with CAS")
	}
	material.response.Generation = 1
	if err := sync.Ensure(ctx, spec); err == nil {
		t.Fatal("credential downgrade accepted")
	}
	material.response.Generation = 2
	for _, tc := range []struct {
		name   string
		mutate func(*corev1.Secret)
	}{
		{"tenant", func(s *corev1.Secret) { s.Labels[tenantIDLabel] = "foreign" }},
		{"space", func(s *corev1.Secret) { s.Annotations[imageSpaceAnnotation] = "foreign" }},
		{"registry", func(s *corev1.Secret) { s.Annotations[imageRegistryAnnotation] = "foreign.test" }},
		{"owner", func(s *corev1.Secret) { s.Labels["app.kubernetes.io/managed-by"] = "ani-resource-instance" }},
		{"owner-reference", func(s *corev1.Secret) {
			s.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "other", UID: "other"}}
		}},
		{"same-generation-corruption", func(s *corev1.Secret) { s.Data[corev1.DockerConfigJsonKey] = []byte("corrupt") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			foreign := updated.DeepCopy()
			tc.mutate(foreign)
			isolated := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
			if err := (&SharedPullSecrets{Client: isolated, Material: material}).Ensure(ctx, spec); err == nil {
				t.Fatal("foreign or corrupted Secret adopted")
			}
			var after corev1.Secret
			if err := isolated.Get(ctx, key, &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after.Data, foreign.Data) || !reflect.DeepEqual(after.Labels, foreign.Labels) {
				t.Fatal("refused Secret was modified")
			}
		})
	}
}

type conflictingSecretClient struct {
	client.Client
	attempted bool
}

func (c *conflictingSecretClient) Update(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
	c.attempted = obj.GetResourceVersion() != ""
	return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, obj.GetName(), errors.New("concurrent credential writer"))
}

func TestSharedPullSecretRejectsWrongMaterialAndConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	spec := imagePullTestSpec()
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	for _, tc := range []struct {
		name   string
		mutate func(*imagev1.GetTenantPullMaterialResponse)
	}{
		{"foreign-tenant", func(m *imagev1.GetTenantPullMaterialResponse) { m.TenantId = "33333333-3333-4333-8333-333333333333" }},
		{"foreign-registry", func(m *imagev1.GetTenantPullMaterialResponse) { m.RegistryAuthority = "foreign.test" }},
		{"expired", func(m *imagev1.GetTenantPullMaterialResponse) {
			m.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
		}},
		{"no-generation", func(m *imagev1.GetTenantPullMaterialResponse) { m.Generation = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pullMaterialFixture()
			tc.mutate(m)
			k := fake.NewClientBuilder().WithScheme(scheme).Build()
			if err := (&SharedPullSecrets{Client: k, Material: &pullMaterialFake{response: m}}).Ensure(ctx, spec); err == nil {
				t.Fatal("wrong material accepted")
			}
			var secrets corev1.SecretList
			if err := k.List(ctx, &secrets); err != nil || len(secrets.Items) != 0 {
				t.Fatal("invalid material created Secret")
			}
		})
	}
	k := fake.NewClientBuilder().WithScheme(scheme).Build()
	material := &pullMaterialFake{response: pullMaterialFixture()}
	if err := (&SharedPullSecrets{Client: k, Material: material}).Ensure(ctx, spec); err != nil {
		t.Fatal(err)
	}
	material.response.Generation = 2
	material.response.Secret = "replacement"
	conflict := &conflictingSecretClient{Client: k}
	if err := (&SharedPullSecrets{Client: conflict, Material: material}).Ensure(ctx, spec); !apierrors.IsConflict(err) || !conflict.attempted {
		t.Fatal("CAS conflict was hidden", err)
	}
	foreign := spec
	foreign.TenantID = "33333333-3333-4333-8333-333333333333"
	if err := (&SharedPullSecrets{Client: k, Material: material}).Ensure(ctx, foreign); err == nil {
		t.Fatal("cross-tenant Secret reference accepted")
	}
}

func TestImagePullProjectionAcrossRuntimeKinds(t *testing.T) {
	spec := imagePullTestSpec()
	assertPod := func(t *testing.T, p *corev1.PodSpec) {
		t.Helper()
		if len(p.ImagePullSecrets) != 1 || p.ImagePullSecrets[0].Name != spec.ImageSelection.PullSecretRef || p.NodeSelector["kubernetes.io/arch"] != "amd64" || p.NodeSelector["kubernetes.io/os"] != "linux" || p.AutomountServiceAccountToken == nil || *p.AutomountServiceAccountToken {
			t.Fatalf("incorrect runtime pull projection: %+v", p)
		}
	}
	deployment, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertPod(t, &deployment.Spec.Template.Spec)
	spec.RuntimeMode = "leader_worker_set"
	lws, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertPod(t, &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec)
	spec.ArtifactProvider, spec.ModelClaim = "model", "model-pvc"
	for _, mode := range []string{"deployment", "leader_worker_set"} {
		spec.RuntimeMode = mode
		obj, err := RenderKServeRuntime(spec)
		if err != nil {
			t.Fatal(err)
		}
		paths := [][]string{{"spec", "predictor"}}
		if mode == "leader_worker_set" {
			paths = [][]string{{"spec", "template"}, {"spec", "worker"}}
		}
		for _, path := range paths {
			component, found, err := unstructured.NestedMap(obj.Object, path...)
			if err != nil || !found {
				t.Fatal("missing runtime component", path, err)
			}
			var pod corev1.PodSpec
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(component, &pod); err != nil {
				t.Fatal(err)
			}
			assertPod(t, &pod)
			annotations, _, _ := unstructured.NestedStringMap(component, "annotations")
			if !kservePodPlacementMatches(component, &pod, annotations) {
				t.Fatal("projected placement not recognized")
			}
			pod.ImagePullSecrets = nil
			if kservePodPlacementMatches(component, &pod, annotations) {
				t.Fatal("missing pull Secret accepted by readiness projection")
			}
		}
	}
	spec.ImageSelection.PullSecretRef = "ani-instance-image-pull"
	spec.RuntimeMode = "deployment"
	spec.ModelClaim = ""
	if _, err := Deployment(spec); err == nil {
		t.Fatal("instance-owned Secret reference accepted")
	}
}

type pullSyncProbe struct {
	calls int
	err   error
}

func (p *pullSyncProbe) Ensure(context.Context, RuntimeSpec) error { p.calls++; return p.err }

func TestRuntimeRouterSynchronizesOnlyRunningImageConsumers(t *testing.T) {
	spec := imagePullTestSpec()
	provider := &routerRuntimeFake{name: "deployment"}
	reconcile := &routerReconcileFake{}
	sync := &pullSyncProbe{err: errors.New("registry unavailable")}
	router := &RuntimeRouter{Source: routerSourceFake{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running"}}, Deployment: provider, DeploymentReconcile: reconcile, PullSecrets: sync}
	op := inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1}
	if err := router.ApplyCR(context.Background(), op); err == nil || len(provider.calls) != 0 {
		t.Fatal("runtime applied without pull material")
	}
	if err := router.ApplyRuntime(context.Background(), op); err == nil || len(provider.calls) != 0 {
		t.Fatal("runtime applied without pull material")
	}
	if _, err := router.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: 1}); err == nil || reconcile.calls != 0 {
		t.Fatal("reconcile applied without pull material")
	}
	calls := sync.calls
	if err := router.DeleteRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := router.DeleteCR(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	router.Source = routerSourceFake{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "stopped"}}
	if _, err := router.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if sync.calls != calls {
		t.Fatal("stop/delete depended on registry or touched shared Secret")
	}
}
