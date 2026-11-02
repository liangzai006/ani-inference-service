package kubernetes

import (
	"context"
	"errors"
	"strings"
	"testing"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/liangzai006/ani-inference-service/internal/biz/reconcile"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	imagePullTestCluster   = "cluster-a"
	imagePullTestNamespace = "models"
	imagePullTestNS        = "33333333-3333-4333-8333-333333333333"
	imagePullTestRegistry  = "registry.test"
)

type pullEnsureFake struct {
	response *imagev1.EnsureTenantPullSecretResponse
	err      error
	calls    int
	request  *imagev1.EnsureTenantPullSecretRequest
}

func (f *pullEnsureFake) EnsurePullSecret(_ context.Context, tenant, namespace, namespaceUID, registry string) (*imagev1.EnsureTenantPullSecretResponse, error) {
	f.calls++
	f.request = &imagev1.EnsureTenantPullSecretRequest{TenantId: tenant, ClusterId: imagePullTestCluster, Namespace: namespace, NamespaceUid: namespaceUID, RegistryAuthority: registry}
	return f.response, f.err
}

func imagePullTestSpec() RuntimeSpec {
	spec := RuntimeSpec{
		TenantID:     "11111111-1111-4111-8111-111111111111",
		ServiceID:    "22222222-2222-4222-8222-222222222222",
		Name:         "image-test",
		Namespace:    imagePullTestNamespace,
		ClusterID:    imagePullTestCluster,
		NamespaceUID: imagePullTestNS,
		Image:        imagePullTestRegistry + "/platform/engine@sha256:" + strings.Repeat("a", 64),
		Generation:   1,
		Replicas:     1,
		WorkerReplicas: 1,
	}
	name, err := inferencebiz.PullSecretName(spec.TenantID, spec.ClusterID, spec.Namespace, imagePullTestRegistry)
	if err != nil {
		panic(err)
	}
	spec.ImageSelection = &inferencebiz.ImageSelection{ImageID: "img_" + strings.Repeat("a", 32), Scope: "platform", OS: "linux", Architecture: "amd64", RegistryAuthority: imagePullTestRegistry, PullSecretRef: name}
	return spec
}

func pullSecretFixture(spec RuntimeSpec) *imagev1.EnsureTenantPullSecretResponse {
	return &imagev1.EnsureTenantPullSecretResponse{SecretRef: spec.ImageSelection.PullSecretRef, RegistryAuthority: spec.ImageSelection.RegistryAuthority, Generation: 1}
}

// ensuredSecret is the object Resource would have written. Inference only reads it.
func ensuredSecret(spec RuntimeSpec) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: spec.Namespace, Name: spec.ImageSelection.PullSecretRef},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.test":{}}}`)},
	}
}

// readOnlyKube proves Inference never writes the shared Secret: any mutation is
// a hard failure instead of a silent second writer.
type readOnlyKube struct {
	client.Client
	writes int
}

func (c *readOnlyKube) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	c.writes++
	return errors.New("inference must not create secrets")
}

func (c *readOnlyKube) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.writes++
	return errors.New("inference must not update secrets")
}

func (c *readOnlyKube) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.writes++
	return errors.New("inference must not patch secrets")
}

func (c *readOnlyKube) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	c.writes++
	return errors.New("inference must not delete secrets")
}

func pullSecretFixtureSync(t *testing.T, spec RuntimeSpec) (*ResourcePullSecrets, *pullEnsureFake, *readOnlyKube) {
	t.Helper()
	reader := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(ensuredSecret(spec)).Build()
	kube := &readOnlyKube{Client: reader}
	ensurer := &pullEnsureFake{response: pullSecretFixture(spec)}
	return &ResourcePullSecrets{Ensurer: ensurer, Reader: kube}, ensurer, kube
}

func TestResourcePullSecretEnsuresFrozenTenantScope(t *testing.T) {
	ctx := context.Background()
	spec := imagePullTestSpec()
	sync, ensurer, kube := pullSecretFixtureSync(t, spec)
	for i := 0; i < 2; i++ {
		if err := sync.Ensure(ctx, spec); err != nil {
			t.Fatal(err)
		}
	}
	if ensurer.calls != 2 {
		t.Fatalf("ensure calls = %d", ensurer.calls)
	}
	if got := ensurer.request; got.GetTenantId() != spec.TenantID || got.GetNamespace() != spec.Namespace || got.GetNamespaceUid() != spec.NamespaceUID || got.GetRegistryAuthority() != imagePullTestRegistry {
		t.Fatalf("ensure scope = %+v", got)
	}
	if kube.writes != 0 {
		t.Fatal("inference wrote the shared Secret")
	}
	if strings.Contains(spec.ImageSelection.PullSecretRef, "inference") {
		t.Fatal("secret name must not embed the service kind")
	}
}

func TestResourcePullSecretRejectsReceiptThatDiffersFromFrozenReference(t *testing.T) {
	ctx := context.Background()
	spec := imagePullTestSpec()
	for _, tc := range []struct {
		name   string
		mutate func(*imagev1.EnsureTenantPullSecretResponse)
	}{
		{"secret-ref", func(r *imagev1.EnsureTenantPullSecretResponse) { r.SecretRef = "ani-pull-00000000000000000000000000000000" }},
		{"instance-secret", func(r *imagev1.EnsureTenantPullSecretResponse) { r.SecretRef = "ani-instance-image-pull" }},
		{"registry", func(r *imagev1.EnsureTenantPullSecretResponse) { r.RegistryAuthority = "foreign.test" }},
		{"no-generation", func(r *imagev1.EnsureTenantPullSecretResponse) { r.Generation = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sync, ensurer, kube := pullSecretFixtureSync(t, spec)
			tc.mutate(ensurer.response)
			if err := sync.Ensure(ctx, spec); err == nil {
				t.Fatal("mismatched receipt accepted")
			}
			if kube.writes != 0 {
				t.Fatal("refused receipt modified a Secret")
			}
		})
	}
}

func TestResourcePullSecretRequiresVisibleSecret(t *testing.T) {
	ctx := context.Background()
	spec := imagePullTestSpec()
	reader := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	sync := &ResourcePullSecrets{Ensurer: &pullEnsureFake{response: pullSecretFixture(spec)}, Reader: reader}
	if err := sync.Ensure(ctx, spec); err == nil {
		t.Fatal("missing Secret after ensure accepted")
	}
	wrongType := ensuredSecret(spec)
	wrongType.Type = corev1.SecretTypeOpaque
	reader = fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(wrongType).Build()
	sync = &ResourcePullSecrets{Ensurer: &pullEnsureFake{response: pullSecretFixture(spec)}, Reader: reader}
	if err := sync.Ensure(ctx, spec); err == nil {
		t.Fatal("Secret with unexpected content type accepted")
	}
}

func TestResourcePullSecretRejectsNamesOutsideSharedScope(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		mutate func(*RuntimeSpec)
	}{
		{"legacy-inference-name", func(s *RuntimeSpec) { s.ImageSelection.PullSecretRef = "ani-inference-pull-abc" }},
		{"legacy-instance-name", func(s *RuntimeSpec) { s.ImageSelection.PullSecretRef = "ani-instance-image-pull" }},
		{"other-cluster-name", func(s *RuntimeSpec) {
			name, err := inferencebiz.PullSecretName(s.TenantID, "cluster-b", s.Namespace, imagePullTestRegistry)
			if err != nil {
				t.Fatal(err)
			}
			s.ImageSelection.PullSecretRef = name
		}},
		{"missing-namespace-uid", func(s *RuntimeSpec) { s.NamespaceUID = "" }},
		{"missing-cluster", func(s *RuntimeSpec) { s.ClusterID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := imagePullTestSpec()
			tc.mutate(&spec)
			sync, ensurer, _ := pullSecretFixtureSync(t, spec)
			if err := sync.Ensure(ctx, spec); err == nil {
				t.Fatal("non-shared scope accepted")
			}
			if ensurer.calls != 0 {
				t.Fatal("invalid scope reached Resource")
			}
		})
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
		t.Fatal("runtime applied without pull Secret")
	}
	if err := router.ApplyRuntime(context.Background(), op); err == nil || len(provider.calls) != 0 {
		t.Fatal("runtime applied without pull Secret")
	}
	if _, err := router.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: 1}); err == nil || reconcile.calls != 0 {
		t.Fatal("reconcile applied without pull Secret")
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

func TestResourcePullSecretClassifiesOnlyTransientFailures(t *testing.T) {
	ctx := context.Background()
	spec := imagePullTestSpec()
	for _, tc := range []struct {
		name      string
		err       error
		transient bool
	}{
		{"unavailable", status.Error(codes.Unavailable, "down"), true},
		{"deadline", status.Error(codes.DeadlineExceeded, "slow"), true},
		{"permission", status.Error(codes.PermissionDenied, "denied"), false},
		{"not-found", status.Error(codes.NotFound, "missing"), false},
		{"unknown", errors.New("unclassified"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sync, ensurer, _ := pullSecretFixtureSync(t, spec)
			ensurer.err = tc.err
			err := sync.Ensure(ctx, spec)
			if err == nil {
				t.Fatal("failure ignored")
			}
			var unavailable *pullSecretUnavailableError
			if errors.As(err, &unavailable) != tc.transient {
				t.Fatalf("transient classification = %v for %v", !tc.transient, tc.err)
			}
		})
	}
}
