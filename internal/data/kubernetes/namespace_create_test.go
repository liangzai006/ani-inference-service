package kubernetes

import (
	"context"
	"errors"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTenantRuntimePrerequisitesPrepareFirstUseAndReuse(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "ani-system", Name: "runtime"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ani-system", Name: "ca"}, Data: map[string]string{"ca.crt": "public-ca"}},
	).Build()
	p := TenantRuntimePrerequisites{Reader: kube, Writer: kube, ServiceAccountName: "runtime", DownloadCAConfigMap: "ca", DownloadCAKey: "ca.crt",
		SourceNamespace: "ani-system", ControllerServiceAccount: "control", RuntimeRole: "runtime-role", PullSecretRole: "pull-role", ResourceServiceAccount: "resource"}
	ns := TenantNamespace{Name: "existing-tenant", UID: "original-uid"}
	for i := 0; i < 2; i++ {
		if err := p.Check(context.Background(), ns); err != nil {
			t.Fatal(err)
		}
	}
	var sa corev1.ServiceAccount
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: "runtime"}, &sa); err != nil || sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Fatalf("missing or token-bearing runtime account: %+v %v", sa, err)
	}
	var ca corev1.ConfigMap
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: "ca"}, &ca); err != nil || ca.Data["ca.crt"] != "public-ca" {
		t.Fatalf("public CA not prepared: %+v %v", ca, err)
	}
	for role, account := range map[string]string{"runtime-role": "control", "pull-role": "resource"} {
		var binding rbacv1.RoleBinding
		if err := kube.Get(context.Background(), client.ObjectKey{Namespace: ns.Name, Name: role + "-tenant"}, &binding); err != nil {
			t.Fatal(err)
		}
		if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != role || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != "ani-system" || binding.Subjects[0].Name != account {
			t.Fatalf("incorrect runtime binding: %+v", binding)
		}
	}
}

type namespaceResolverStub struct {
	ns    TenantNamespace
	err   error
	calls int
}

func TestTenantRuntimePrerequisitesRequireServiceAccountAndCA(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-space", Name: "runtime"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-space", Name: "ca"}, Data: map[string]string{"ca.crt": "certificate"}},
	).Build()
	check := TenantRuntimePrerequisites{Reader: reader, ServiceAccountName: "runtime", DownloadCAConfigMap: "ca", DownloadCAKey: "ca.crt"}
	ns := TenantNamespace{Name: "tenant-space", UID: "uid"}
	if err := check.Check(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
	check.DownloadCAKey = "wrong"
	if err := check.Check(context.Background(), ns); err == nil {
		t.Fatal("missing CA key accepted")
	}
	check.DownloadCAKey, check.ServiceAccountName = "ca.crt", "missing"
	if err := check.Check(context.Background(), ns); err == nil {
		t.Fatal("missing service account accepted")
	}
}

func (r *namespaceResolverStub) Resolve(context.Context, string) (TenantNamespace, error) {
	r.calls++
	return r.ns, r.err
}

type namespaceCreateStub struct {
	handled bool
	input   biz.CreateInput
	calls   int
}

func (s *namespaceCreateStub) PrepareCreate(context.Context, biz.CreateInput) (*inferencev1.OperationResponse, bool, error) {
	return &inferencev1.OperationResponse{}, s.handled, nil
}
func (s *namespaceCreateStub) Create(_ context.Context, in biz.CreateInput) (*inferencev1.OperationResponse, error) {
	s.calls++
	s.input = in
	return &inferencev1.OperationResponse{}, nil
}

func TestNamespaceCreateResolvesBeforeAdmissionAndReplaySkipsResolution(t *testing.T) {
	resolver := &namespaceResolverStub{ns: TenantNamespace{Name: "platform-owned", UID: "uid"}}
	store := &namespaceCreateStub{}
	uc := &NamespaceCreate{Resolver: resolver, Preflight: store, Next: store}
	_, err := uc.Create(context.Background(), biz.CreateInput{TenantID: "tenant", RuntimeNamespace: "untrusted"})
	if err != nil || store.input.RuntimeNamespace != "platform-owned" || store.input.RuntimeNamespaceUID != "uid" {
		t.Fatalf("placement: %+v, %v", store.input, err)
	}
	store.handled = true
	if _, err := uc.Create(context.Background(), biz.CreateInput{}); err != nil || resolver.calls != 1 || store.calls != 1 {
		t.Fatalf("replay did external work: %v", err)
	}
	store.handled = false
	resolver.err = errors.New("namespace unavailable")
	if _, err := uc.Create(context.Background(), biz.CreateInput{}); err == nil || store.calls != 1 {
		t.Fatal("missing namespace accepted")
	}
}
