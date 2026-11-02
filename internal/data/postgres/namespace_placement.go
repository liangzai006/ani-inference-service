package postgres

import (
	"context"
	"errors"

	kube "github.com/liangzai006/ani-inference-service/internal/data/kubernetes"
)

func (r *Repository) GetNamespacePlacement(ctx context.Context, tenantID, serviceID string) (kube.TenantNamespace, error) {
	if r == nil || r.pool == nil {
		return kube.TenantNamespace{}, errors.New("nil namespace placement repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return kube.TenantNamespace{}, err
	}
	service, err := parseUUID("service_id", serviceID, false)
	if err != nil {
		return kube.TenantNamespace{}, err
	}
	row, err := New(r.pool).GetNamespacePlacement(ctx, GetNamespacePlacementParams{TenantID: tenant, ServiceID: service})
	if err != nil {
		return kube.TenantNamespace{}, err
	}
	return kube.TenantNamespace{Name: row.NamespaceName, UID: row.NamespaceUid}, nil
}
