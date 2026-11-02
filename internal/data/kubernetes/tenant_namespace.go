package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	tenantNamespaceOwnerLabel = "network.ani.io/managed-by"
	tenantNamespaceOwner      = "ani-network-service"
	tenantNamespaceIDLabel    = "network.ani.io/tenant-id"
	tenantNamespacePrefix     = "tenant-"
)

// TenantNamespace is an existing platform-owned namespace, not an instruction
// to create or rename a namespace. UID prevents a recreated namespace being
// mistaken for the original after a durable operation has pinned it.
type TenantNamespace struct {
	Name string
	UID  string
}

// TenantNamespaceResolver discovers the platform-owned namespace by its
// ownership labels. It never constructs a namespace name from the tenant ID.
type TenantNamespaceResolver struct {
	Reader client.Reader
}

func (r TenantNamespaceResolver) Resolve(ctx context.Context, tenant string) (TenantNamespace, error) {
	if r.Reader == nil {
		return TenantNamespace{}, errors.New("tenant namespace reader is unavailable")
	}
	id, err := uuid.Parse(tenant)
	if err != nil || id == uuid.Nil || id.String() != tenant {
		return TenantNamespace{}, errors.New("tenant namespace requires a canonical tenant UUID")
	}
	var list corev1.NamespaceList
	if err := r.Reader.List(ctx, &list, client.MatchingLabels{
		tenantNamespaceOwnerLabel: tenantNamespaceOwner,
		tenantNamespaceIDLabel:    tenant,
	}); err != nil {
		return TenantNamespace{}, fmt.Errorf("discover tenant namespace: %w", err)
	}
	if len(list.Items) != 1 {
		return TenantNamespace{}, fmt.Errorf("expected one platform-owned tenant namespace, found %d", len(list.Items))
	}
	ns := &list.Items[0]
	// Validate the platform's format; do not synthesize or accept a caller's
	// namespace. Both the name and ownership labels must refer to this tenant.
	if len(ns.Name) != 43 || !strings.HasPrefix(ns.Name, tenantNamespacePrefix) || ns.Name[len(tenantNamespacePrefix):] != tenant ||
		ns.UID == "" || ns.DeletionTimestamp != nil || ns.Status.Phase != corev1.NamespaceActive ||
		ns.Labels[tenantNamespaceOwnerLabel] != tenantNamespaceOwner || ns.Labels[tenantNamespaceIDLabel] != tenant {
		return TenantNamespace{}, errors.New("platform-owned tenant namespace is invalid or unavailable")
	}
	return TenantNamespace{Name: ns.Name, UID: string(ns.UID)}, nil
}

// Verify refuses to adopt a namespace recreated after the original placement
// was durably pinned, even if its name and labels are the same.
func (r TenantNamespaceResolver) Verify(ctx context.Context, tenant string, pinned TenantNamespace) error {
	if pinned.Name == "" || pinned.UID == "" {
		return errors.New("pinned tenant namespace identity is required")
	}
	actual, err := r.Resolve(ctx, tenant)
	if err != nil {
		return err
	}
	if actual != pinned {
		return errors.New("tenant namespace identity changed")
	}
	return nil
}
