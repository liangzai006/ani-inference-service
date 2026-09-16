package postgres

import (
	"context"
	"errors"

	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

// StatusProjectionSource is the read-only PostgreSQL adapter used by the CRD
// status projector. It never advances work, settles quota, or mutates a
// domain row while constructing the snapshot.
type StatusProjectionSource struct{ Pool DBTX }

func NewStatusProjectionSource(pool DBTX) *StatusProjectionSource {
	return &StatusProjectionSource{Pool: pool}
}

var _ kube.StatusProjectionSource = (*StatusProjectionSource)(nil)

func (s *StatusProjectionSource) GetStatusProjection(ctx context.Context, tenantID, serviceID string) (kube.StatusProjection, error) {
	if s == nil || s.Pool == nil {
		return kube.StatusProjection{}, errors.New("nil postgres status projection source")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return kube.StatusProjection{}, err
	}
	service, err := workUUID("service_id", serviceID)
	if err != nil {
		return kube.StatusProjection{}, err
	}
	q := New(s.Pool)
	snapshot, err := q.GetStatusProjection(ctx, GetStatusProjectionParams{TenantID: tenant, ServiceID: service})
	if err != nil {
		return kube.StatusProjection{}, err
	}
	projection := kube.StatusProjection{
		AppliedGeneration:    snapshot.AppliedGeneration,
		RuntimePhase:         snapshot.RuntimePhase,
		ReadyReplicas:        snapshot.ReadyReplicas,
		ModelReady:           snapshot.ModelReady,
		PublicationPhase:     snapshot.PublicationPhase,
		PublicationEffective: snapshot.PublicationEffective,
		InvocationHealth:     snapshot.InvocationHealth,
	}
	if snapshot.ControlGeneration > 0 {
		projection.ControlBinding = &kube.RuntimeBinding{
			Kind: "InferenceService", Role: "control", Generation: snapshot.ControlGeneration,
			Namespace: snapshot.ControlNamespace, Name: snapshot.ControlName,
			UID: snapshot.ControlUid,
		}
	}
	if snapshot.ObservedAt.Valid {
		t := snapshot.ObservedAt.Time
		projection.ObservedAt = &t
	}
	if snapshot.InvocationObservedAt.Valid {
		t := snapshot.InvocationObservedAt.Time
		projection.InvocationObservedAt = &t
	}
	if snapshot.RuntimeStaleAfter.Valid {
		t := snapshot.RuntimeStaleAfter.Time
		projection.RuntimeStaleAfter = &t
	}
	if snapshot.PublicationObservedAt.Valid {
		t := snapshot.PublicationObservedAt.Time
		projection.PublicationObservedAt = &t
	}
	if snapshot.OperationID.Valid {
		projection.Operation = &kube.OperationProjection{
			ID: snapshot.OperationID.String(), Kind: snapshot.OperationKind.String,
			Phase: snapshot.OperationPhase.String, Step: snapshot.OperationStep.String,
			Message: snapshot.OperationMessage.String,
		}
	}
	return projection, nil
}
