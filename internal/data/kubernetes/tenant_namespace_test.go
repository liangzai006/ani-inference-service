package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTenantNamespaceResolverUsesPlatformOwnership(t *testing.T) {
	const tenant = "64c5c35a-f554-432c-b4bc-7b4f36f08420"
	const name = "tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420"
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	valid := func() *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("namespace-uid"), Labels: map[string]string{tenantNamespaceOwnerLabel: tenantNamespaceOwner, tenantNamespaceIDLabel: tenant}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}
	}
	tests := []struct {
		name       string
		namespaces []*corev1.Namespace
		tenant     string
		want       bool
	}{
		{name: "owned namespace", namespaces: []*corev1.Namespace{valid()}, tenant: tenant, want: true},
		{name: "missing", tenant: tenant},
		{name: "invalid tenant", namespaces: []*corev1.Namespace{valid()}, tenant: "tenant-a"},
		{name: "wrong owner", namespaces: []*corev1.Namespace{func() *corev1.Namespace { n := valid(); n.Labels[tenantNamespaceOwnerLabel] = "other"; return n }()}, tenant: tenant},
		{name: "wrong name", namespaces: []*corev1.Namespace{func() *corev1.Namespace { n := valid(); n.Name = "tenant-other"; return n }()}, tenant: tenant},
		{name: "missing uid", namespaces: []*corev1.Namespace{func() *corev1.Namespace { n := valid(); n.UID = ""; return n }()}, tenant: tenant},
		{name: "inactive", namespaces: []*corev1.Namespace{func() *corev1.Namespace { n := valid(); n.Status.Phase = corev1.NamespaceTerminating; return n }()}, tenant: tenant},
		{name: "ambiguous labels", namespaces: []*corev1.Namespace{valid(), func() *corev1.Namespace { n := valid(); n.Name = "tenant-unexpected"; return n }()}, tenant: tenant},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, ns := range tc.namespaces {
				builder.WithObjects(ns)
			}
			got, err := (TenantNamespaceResolver{Reader: builder.Build()}).Resolve(context.Background(), tc.tenant)
			if (err == nil) != tc.want {
				t.Fatalf("Resolve() = %+v, %v; want success %v", got, err, tc.want)
			}
			if tc.want && (got.Name != name || got.UID != "namespace-uid") {
				t.Fatalf("unexpected resolution: %+v", got)
			}
		})
	}
}

func TestTenantNamespaceResolverDetectsReplacement(t *testing.T) {
	const tenant = "64c5c35a-f554-432c-b4bc-7b4f36f08420"
	const name = "tenant-64c5c35a-f554-432c-b4bc-7b4f36f08420"
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("new-uid"), Labels: map[string]string{tenantNamespaceOwnerLabel: tenantNamespaceOwner, tenantNamespaceIDLabel: tenant}}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}
	r := TenantNamespaceResolver{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()}
	if err := r.Verify(context.Background(), tenant, TenantNamespace{Name: name, UID: "old-uid"}); err == nil {
		t.Fatal("a recreated namespace must not match the persisted UID")
	}
	if err := r.Verify(context.Background(), tenant, TenantNamespace{Name: name, UID: "new-uid"}); err != nil {
		t.Fatal(err)
	}
}
