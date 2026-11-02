package kubernetes

import (
	"context"
	"errors"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

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
