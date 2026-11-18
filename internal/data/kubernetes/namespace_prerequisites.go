package kubernetes

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TenantRuntimePrerequisites checks tenant-local runtime dependencies before
// accepting an operation, so missing platform provisioning is visible to the
// caller instead of leaving a worker retrying indefinitely.
type TenantRuntimePrerequisites struct {
	Reader                                                 client.Reader
	ServiceAccountName, DownloadCAConfigMap, DownloadCAKey string
	Writer                                                 client.Client
	SourceNamespace, ControllerServiceAccount              string
	RuntimeRole, PullSecretRole, ResourceServiceAccount    string
}

func (p TenantRuntimePrerequisites) Check(ctx context.Context, ns TenantNamespace) error {
	if p.Reader == nil || ns.Name == "" || ns.UID == "" {
		return fmt.Errorf("tenant runtime prerequisites are not configured")
	}
	if p.Writer != nil {
		if err := p.prepare(ctx, ns); err != nil {
			return err
		}
	}
	if name := strings.TrimSpace(p.ServiceAccountName); name != "" {
		var sa corev1.ServiceAccount
		if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: name}, &sa); err != nil {
			return fmt.Errorf("tenant runtime service account %s/%s is unavailable: %w", ns.Name, name, err)
		}
	}
	if name := strings.TrimSpace(p.DownloadCAConfigMap); name != "" {
		var cm corev1.ConfigMap
		if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: name}, &cm); err != nil {
			return fmt.Errorf("tenant model-download CA %s/%s is unavailable: %w", ns.Name, name, err)
		}
		key := strings.TrimSpace(p.DownloadCAKey)
		if key == "" {
			key = "ca-certificates.crt"
		}
		if cm.Data[key] == "" && len(cm.BinaryData[key]) == 0 {
			return fmt.Errorf("tenant model-download CA %s/%s is missing key %s", ns.Name, name, key)
		}
	}
	return nil
}

// prepare runs synchronously after the original tenant Namespace resolver.
// It reuses the installed runtime identity and public CA; it never creates a
// Namespace or handles registry credentials.
func (p TenantRuntimePrerequisites) prepare(ctx context.Context, ns TenantNamespace) error {
	if p.SourceNamespace == "" || p.ControllerServiceAccount == "" || p.RuntimeRole == "" || p.PullSecretRole == "" || p.ResourceServiceAccount == "" {
		return fmt.Errorf("tenant runtime preparation configuration is incomplete")
	}
	if name := strings.TrimSpace(p.ServiceAccountName); name != "" {
		var source corev1.ServiceAccount
		if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: p.SourceNamespace, Name: name}, &source); err != nil {
			return fmt.Errorf("read source runtime service account: %w", err)
		}
		var account corev1.ServiceAccount
		key := client.ObjectKey{Namespace: ns.Name, Name: name}
		err := p.Writer.Get(ctx, key, &account)
		if apierrors.IsNotFound(err) {
			noToken := false
			account = corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name}, AutomountServiceAccountToken: &noToken, ImagePullSecrets: source.ImagePullSecrets}
			if err = p.Writer.Create(ctx, &account); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("prepare tenant runtime service account: %w", err)
			}
		} else if err != nil {
			return err
		}
	}
	if name := strings.TrimSpace(p.DownloadCAConfigMap); name != "" {
		var source corev1.ConfigMap
		if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: p.SourceNamespace, Name: name}, &source); err != nil {
			return fmt.Errorf("read source model-download CA: %w", err)
		}
		var target corev1.ConfigMap
		err := p.Writer.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: name}, &target)
		if apierrors.IsNotFound(err) {
			target = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name}, Data: source.Data, BinaryData: source.BinaryData}
			if err = p.Writer.Create(ctx, &target); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("prepare tenant model-download CA: %w", err)
			}
		} else if err != nil {
			return err
		}
	}
	for _, binding := range []struct{ role, account string }{
		{p.RuntimeRole, p.ControllerServiceAccount},
		{p.PullSecretRole, p.ResourceServiceAccount},
	} {
		name := binding.role + "-tenant"
		var current rbacv1.RoleBinding
		err := p.Writer.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: name}, &current)
		want := rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: binding.role},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: binding.account, Namespace: p.SourceNamespace}},
		}
		if apierrors.IsNotFound(err) {
			if err = p.Writer.Create(ctx, &want); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("prepare tenant runtime binding: %w", err)
			}
		} else if err != nil {
			return err
		} else if current.RoleRef != want.RoleRef {
			return fmt.Errorf("tenant runtime binding %s references a different role", name)
		} else if len(current.Subjects) != 1 || current.Subjects[0] != want.Subjects[0] {
			current.Subjects = want.Subjects
			if err = p.Writer.Update(ctx, &current); err != nil {
				return fmt.Errorf("update tenant runtime binding: %w", err)
			}
		}
	}
	return nil
}
