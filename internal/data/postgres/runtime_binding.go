package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

// ErrRuntimeBindingCAS reports that the binding changed since the caller's
// read. Callers must re-read the Kubernetes object and binding before trying
// again; they must never overwrite a binding on a blind retry.
var ErrRuntimeBindingCAS = errors.New("runtime binding CAS conflict")

// RuntimeBindingInput is the durable identity observed for one object. The
// expected UID/resourceVersion pair is the fencing token read immediately
// before an update. Both expected values must be empty for a first insert and
// both must be supplied for an update. Replaying the exact initial binding is
// idempotent even when both expected values are empty.
type RuntimeBindingInput struct {
	TenantID        string
	ServiceID       string
	Generation      int64
	ObjectKind      string
	ObjectNamespace string
	ObjectName      string
	ObjectUID       string
	ResourceVersion string
	Role            string

	ExpectedUID             string
	ExpectedResourceVersion string
}

// UpsertRuntimeBinding adapts the Kubernetes executor's narrow persistence
// contract to the repository CAS implementation.
func (r *Repository) UpsertRuntimeBinding(ctx context.Context, in kubernetes.BindingRecord) error {
	return r.UpsertRuntimeBindingCAS(ctx, RuntimeBindingInput{
		TenantID: in.TenantID, ServiceID: in.ServiceID, Generation: in.Generation,
		ObjectKind: in.Kind, ObjectNamespace: in.Namespace, ObjectName: in.Name,
		ObjectUID: in.UID, ResourceVersion: in.ResourceVersion, Role: in.Role,
		ExpectedUID: in.ExpectedUID, ExpectedResourceVersion: in.ExpectedResourceVersion,
	})
}

// UpsertRuntimeBindingCAS creates a binding or advances its resource version
// under a strict UID/resourceVersion compare-and-swap. A binding's object UID,
// namespace and name are immutable for a generation. This prevents a delayed
// event for an old object (or same-name replacement) from taking ownership.
func (r *Repository) UpsertRuntimeBindingCAS(ctx context.Context, in RuntimeBindingInput) error {
	if r == nil || r.pool == nil {
		return errors.New("nil postgres runtime binding repository")
	}
	if err := validateRuntimeBindingInput(in); err != nil {
		return err
	}
	tenant, err := parseUUID("tenant_id", in.TenantID, false)
	if err != nil {
		return err
	}
	service, err := parseUUID("service_id", in.ServiceID, false)
	if err != nil {
		return err
	}
	rows, err := New(r.pool).UpsertRuntimeBindingCAS(ctx, UpsertRuntimeBindingCASParams{
		TenantID: tenant, ServiceID: service, Generation: in.Generation,
		ObjectKind: in.ObjectKind, ObjectNamespace: in.ObjectNamespace,
		ObjectName: in.ObjectName, ObjectUid: in.ObjectUID,
		ResourceVersion: in.ResourceVersion, Role: in.Role,
		ExpectedUid: in.ExpectedUID, ExpectedResourceVersion: in.ExpectedResourceVersion,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrRuntimeBindingCAS
	}
	return nil
}

// GetRuntimeBinding reads one tenant-scoped binding. pgx.ErrNoRows is kept so
// callers can distinguish an unbound object from an authorization failure.
func (r *Repository) GetRuntimeBinding(ctx context.Context, tenantID, serviceID string, generation int64, objectKind, role string) (InferenceRuntimeBinding, error) {
	if r == nil || r.pool == nil {
		return InferenceRuntimeBinding{}, errors.New("nil postgres runtime binding repository")
	}
	if generation < 1 || strings.TrimSpace(objectKind) == "" || strings.TrimSpace(role) == "" {
		return InferenceRuntimeBinding{}, fmt.Errorf("generation, object kind and role are required")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return InferenceRuntimeBinding{}, err
	}
	service, err := parseUUID("service_id", serviceID, false)
	if err != nil {
		return InferenceRuntimeBinding{}, err
	}
	return New(r.pool).GetRuntimeBinding(ctx, GetRuntimeBindingParams{
		TenantID: tenant, ServiceID: service, Generation: generation,
		ObjectKind: objectKind, Role: role,
	})
}

// DeleteRuntimeBindingCAS removes a binding only when the caller still holds
// the exact object UID and resourceVersion that it read.
func (r *Repository) DeleteRuntimeBindingCAS(ctx context.Context, tenantID, serviceID string, generation int64, objectKind, role, objectUID, resourceVersion string) error {
	if r == nil || r.pool == nil {
		return errors.New("nil postgres runtime binding repository")
	}
	if generation < 1 || strings.TrimSpace(objectKind) == "" || strings.TrimSpace(role) == "" || strings.TrimSpace(objectUID) == "" || strings.TrimSpace(resourceVersion) == "" {
		return fmt.Errorf("generation, object kind, role, UID and resourceVersion are required")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return err
	}
	service, err := parseUUID("service_id", serviceID, false)
	if err != nil {
		return err
	}
	rows, err := New(r.pool).DeleteRuntimeBindingCAS(ctx, DeleteRuntimeBindingCASParams{
		TenantID: tenant, ServiceID: service, Generation: generation,
		ObjectKind: objectKind, Role: role, ObjectUid: objectUID,
		ResourceVersion: resourceVersion,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrRuntimeBindingCAS
	}
	return nil
}

func validateRuntimeBindingInput(in RuntimeBindingInput) error {
	if in.Generation < 1 {
		return fmt.Errorf("generation must be positive")
	}
	for label, value := range map[string]string{
		"object_kind": in.ObjectKind, "object_namespace": in.ObjectNamespace,
		"object_name": in.ObjectName, "object_uid": in.ObjectUID,
		"resource_version": in.ResourceVersion, "role": in.Role,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if (in.ExpectedUID == "") != (in.ExpectedResourceVersion == "") {
		return fmt.Errorf("expected UID and expected resourceVersion must be supplied together")
	}
	return nil
}
