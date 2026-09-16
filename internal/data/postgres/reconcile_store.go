package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

// ReconcileStore is the PostgreSQL side of the infrastructure-neutral
// reconciler. It reads only the current tenant-scoped aggregate and commits
// observations with the desired-generation CAS query.
type ReconcileStore struct{ pool *pgxpool.Pool }

func NewReconcileStore(pool *pgxpool.Pool) *ReconcileStore { return &ReconcileStore{pool: pool} }

var _ bizreconcile.Repository = (*ReconcileStore)(nil)

func (s *ReconcileStore) Current(ctx context.Context, tenantID, serviceID string) (bizreconcile.Desired, error) {
	if s == nil || s.pool == nil {
		return bizreconcile.Desired{}, errors.New("nil postgres reconcile store")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	service, err := workUUID("service_id", serviceID)
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	row, err := New(s.pool).GetService(ctx, GetServiceParams{TenantID: tenant, ID: service})
	if errors.Is(err, pgx.ErrNoRows) {
		return bizreconcile.Desired{}, fmt.Errorf("inference service %s/%s not found", tenantID, serviceID)
	}
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	return bizreconcile.Desired{TenantID: tenantID, ServiceID: serviceID, Generation: row.DesiredGeneration}, nil
}

func (s *ReconcileStore) CurrentForWork(ctx context.Context, item work.Item) (bizreconcile.Desired, error) {
	if s == nil || s.pool == nil {
		return bizreconcile.Desired{}, errors.New("nil postgres reconcile store")
	}
	tenant, err := tenantUUID(item.TenantID)
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	service, err := workUUID("service_id", item.ServiceID)
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	lease, err := workUUID("lease_token", item.LeaseToken)
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	row, err := New(s.pool).GetLeasedDesired(ctx, GetLeasedDesiredParams{TenantID: tenant, ID: service, LeaseToken: lease})
	if errors.Is(err, pgx.ErrNoRows) {
		return bizreconcile.Desired{}, bizreconcile.ErrStaleGeneration
	}
	if err != nil {
		return bizreconcile.Desired{}, err
	}
	return bizreconcile.Desired{TenantID: row.TenantID.String(), ServiceID: row.ID.String(), Generation: row.DesiredGeneration}, nil
}

func (s *ReconcileStore) SaveObservation(ctx context.Context, tenantID, serviceID string, generation int64, observation bizreconcile.Observation) error {
	if s == nil || s.pool == nil {
		return errors.New("nil postgres reconcile store")
	}
	if generation < 1 || observation.Generation != generation {
		return bizreconcile.ErrStaleGeneration
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return err
	}
	service, err := workUUID("service_id", serviceID)
	if err != nil {
		return err
	}
	runtimeMode := observation.RuntimeMode
	if runtimeMode == "" {
		runtimeMode = "deployment"
	}
	runtimePhase := observation.RuntimePhase
	if runtimePhase == "" {
		runtimePhase = "unknown"
	}
	publication := observation.PublicationPhase
	if publication == "" {
		publication = "unknown"
	}
	health := observation.InvocationHealth
	if health == "" {
		health = "unknown"
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	modelReady := observation.ModelReady
	if current, currentErr := q.GetRuntime(ctx, GetRuntimeParams{TenantID: tenant, ServiceID: service}); currentErr == nil {
		if !observation.ModelReadyKnown {
			modelReady = current.ModelReady
		}
		if observation.PublicationPhase == "" || observation.PublicationPhase == "unknown" {
			publication = current.PublicationPhase
		}
		// Empty means that this observer did not provide a fact. Preserve an
		// existing fact only within the same generation. Explicit unknown is a
		// real observation result and must not be replaced by an older healthy
		// value; a new generation also starts unknown until it is probed.
		if observation.InvocationHealth == "" && current.Generation == generation {
			health = current.InvocationHealth
		}
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return currentErr
	}
	rows, err := q.UpdateObservationCAS(ctx, UpdateObservationCASParams{
		Generation: generation, RuntimeMode: runtimeMode,
		ReadyGroups: observation.ReadyGroups, ReadyWorkers: observation.ReadyWorkers,
		LwsUid: observation.LWSUID, RuntimePhase: runtimePhase,
		ReadyReplicas: observation.ReadyReplicas, ModelReady: modelReady,
		PublicationPhase: publication, InvocationHealth: health, Reason: observation.Reason,
		TenantID: tenant, ServiceID: service,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return bizreconcile.ErrStaleGeneration
	}
	if rows, err := q.MarkServiceAppliedGeneration(ctx, MarkServiceAppliedGenerationParams{TenantID: tenant, ServiceID: service, Generation: generation}); err != nil {
		return err
	} else if rows != 1 {
		return bizreconcile.ErrStaleGeneration
	}
	return tx.Commit(ctx)
}

func (s *ReconcileStore) SaveObservationForWork(ctx context.Context, item work.Item, observation bizreconcile.Observation) error {
	if s == nil || s.pool == nil {
		return errors.New("nil postgres reconcile store")
	}
	if item.Generation < 1 || observation.Generation != item.Generation {
		return bizreconcile.ErrStaleGeneration
	}
	tenant, err := tenantUUID(item.TenantID)
	if err != nil {
		return err
	}
	service, err := workUUID("service_id", item.ServiceID)
	if err != nil {
		return err
	}
	lease, err := workUUID("lease_token", item.LeaseToken)
	if err != nil {
		return err
	}
	runtimeMode := observation.RuntimeMode
	if runtimeMode == "" {
		runtimeMode = "deployment"
	}
	runtimePhase := observation.RuntimePhase
	if runtimePhase == "" {
		runtimePhase = "unknown"
	}
	publication := observation.PublicationPhase
	if publication == "" {
		publication = "unknown"
	}
	health := observation.InvocationHealth
	if health == "" {
		health = "unknown"
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	modelReady := observation.ModelReady
	if current, currentErr := New(tx).GetRuntime(ctx, GetRuntimeParams{TenantID: tenant, ServiceID: service}); currentErr == nil {
		if !observation.ModelReadyKnown {
			modelReady = current.ModelReady
		}
		if observation.PublicationPhase == "" || observation.PublicationPhase == "unknown" {
			publication = current.PublicationPhase
		}
		// Empty means no invocation fact was supplied. Preserve it only for the
		// same generation; explicit unknown and a new generation must remain
		// unknown instead of inheriting an older health result.
		if observation.InvocationHealth == "" && current.Generation == item.Generation {
			health = current.InvocationHealth
		}
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return currentErr
	}
	rows, err := New(tx).UpdateObservationForWork(ctx, UpdateObservationForWorkParams{
		Generation: item.Generation, RuntimeMode: runtimeMode,
		ReadyGroups: observation.ReadyGroups, ReadyWorkers: observation.ReadyWorkers,
		LwsUid: observation.LWSUID, RuntimePhase: runtimePhase,
		ReadyReplicas: observation.ReadyReplicas, ModelReady: modelReady,
		PublicationPhase: publication, InvocationHealth: health, Reason: observation.Reason,
		TenantID: tenant, ServiceID: service, LeaseToken: lease,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return bizreconcile.ErrStaleGeneration
	}
	if rows, err := New(tx).MarkServiceAppliedGeneration(ctx, MarkServiceAppliedGenerationParams{TenantID: tenant, ServiceID: service, Generation: item.Generation}); err != nil {
		return err
	} else if rows != 1 {
		return bizreconcile.ErrStaleGeneration
	}
	for _, object := range observation.Objects {
		// A worker may only write bindings for the generation it currently
		// holds. Positive generation alone is insufficient: an old runtime
		// event must never be able to mutate the current generation's
		// observation transaction.
		if object.BindingGeneration != item.Generation || object.Kind == "" || object.Namespace == "" || object.Name == "" || object.UID == "" || object.ResourceVersion == "" || object.Role == "" {
			return bizreconcile.ErrStaleGeneration
		}
		if object.Missing {
			deleted, err := New(tx).DeleteRuntimeBindingCAS(ctx, DeleteRuntimeBindingCASParams{
				TenantID: tenant, ServiceID: service, Generation: object.BindingGeneration,
				ObjectKind: object.Kind, Role: object.Role, ObjectUid: object.UID, ResourceVersion: object.ResourceVersion,
			})
			if err != nil {
				return err
			}
			if deleted != 1 {
				return bizreconcile.ErrStaleGeneration
			}
			continue
		}
		inserted, err := New(tx).UpsertRuntimeBindingCAS(ctx, UpsertRuntimeBindingCASParams{
			TenantID: tenant, ServiceID: service, Generation: object.BindingGeneration,
			ObjectKind: object.Kind, ObjectNamespace: object.Namespace, ObjectName: object.Name,
			ObjectUid: object.UID, ResourceVersion: object.ResourceVersion, Role: object.Role,
			ExpectedUid: object.ExpectedUID, ExpectedResourceVersion: object.ExpectedResourceVersion,
		})
		if err != nil {
			return err
		}
		if inserted != 1 {
			return bizreconcile.ErrStaleGeneration
		}
	}
	// Keep an immutable source history in the same transaction as the
	// generation/lease/binding CAS. If any event write fails, none of the
	// observation projection or binding changes become visible.
	if len(observation.Objects) == 0 {
		if err := writeObservationEvent(ctx, New(tx), tenant, service, item.Generation, "runtime", "", "", observation); err != nil {
			return err
		}
	} else {
		for _, object := range observation.Objects {
			if err := writeObservationEvent(ctx, New(tx), tenant, service, item.Generation, object.Kind, object.UID, object.ResourceVersion, struct {
				Observation bizreconcile.Observation   `json:"observation"`
				Object      bizreconcile.RuntimeObject `json:"object"`
			}{Observation: observation, Object: object}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func writeObservationEvent(ctx context.Context, q *Queries, tenant, service pgtype.UUID, generation int64, sourceKind, sourceUID, resourceVersion string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode observation event: %w", err)
	}
	return q.AppendObservationEvent(ctx, AppendObservationEventParams{
		TenantID: tenant, EventID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, ServiceID: service,
		Generation: generation, SourceKind: sourceKind, SourceUid: sourceUID,
		SourceResourceVersion: resourceVersion, Payload: encoded,
	})
}
