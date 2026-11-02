package kubernetes

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TenantRuntimePrerequisites checks tenant-local runtime dependencies before
// accepting an operation, so missing platform provisioning is visible to the
// caller instead of leaving a worker retrying indefinitely.
type TenantRuntimePrerequisites struct {
	Reader                                                 client.Reader
	ServiceAccountName, DownloadCAConfigMap, DownloadCAKey string
}

func (p TenantRuntimePrerequisites) Check(ctx context.Context, ns TenantNamespace) error {
	if p.Reader == nil || ns.Name == "" || ns.UID == "" {
		return fmt.Errorf("tenant runtime prerequisites are not configured")
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
