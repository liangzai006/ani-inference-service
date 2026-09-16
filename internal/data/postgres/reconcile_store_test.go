package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
)

func TestReconcileStoreObservationRequiresCurrentLease(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	if _, err := NewRepository(p).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "reconcile-" + service.String(),
		ModelVersionID: uuid.New().String(), RequestHash: "reconcile-hash", IdempotencyKey: "reconcile-" + service.String(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1",
			"DELETE FROM inference_publications WHERE tenant_id=$1",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
			"DELETE FROM inference_runtime WHERE tenant_id=$1",
			"DELETE FROM inference_specs WHERE tenant_id=$1",
			"DELETE FROM inference_operations WHERE tenant_id=$1",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := p.Exec(context.Background(), statement, pgUUID(tenant)); err != nil {
				t.Errorf("cleanup %q: %v", statement, err)
			}
		}
	})
	store := NewWorkStore(p, WorkStoreOptions{Owner: "reconcile-test", LeaseSeconds: 1, ObserveSeconds: 1, RetrySeconds: .01})
	item, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("Claim() item=%+v ok=%v err=%v", item, ok, err)
	}
	reconcileStore := NewReconcileStore(p)
	desired, err := reconcileStore.CurrentForWork(ctx, item)
	if err != nil || desired.Generation != item.Generation {
		t.Fatalf("CurrentForWork() desired=%+v err=%v", desired, err)
	}
	if err := reconcileStore.SaveObservationForWork(ctx, item, bizreconcile.Observation{
		Generation: item.Generation, RuntimeMode: "deployment", RuntimePhase: "pending",
		Objects: []bizreconcile.RuntimeObject{{BindingGeneration: item.Generation + 1, Kind: "Deployment", Namespace: "ns", Name: "stale", UID: "stale", ResourceVersion: "1", Role: "runtime"}},
	}); !errors.Is(err, bizreconcile.ErrStaleGeneration) {
		t.Fatalf("old-generation binding accepted: %v", err)
	}
	if err := reconcileStore.SaveObservationForWork(ctx, item, bizreconcile.Observation{
		Generation: item.Generation, RuntimeMode: "deployment", RuntimePhase: "pending",
		PublicationPhase: "unknown", InvocationHealth: "unknown", Reason: "test",
		Objects: []bizreconcile.RuntimeObject{{BindingGeneration: item.Generation, Kind: "Deployment", Namespace: "ns", Name: "svc", UID: "runtime-uid", ResourceVersion: "7", Role: "runtime"}},
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := New(p).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.RuntimePhase != "pending" || runtime.Reason != "test" {
		t.Fatalf("observation not persisted: %+v", runtime)
	}
	if _, err := p.Exec(ctx, "UPDATE inference_runtime SET model_ready=true WHERE tenant_id=$1 AND service_id=$2", pgUUID(tenant), pgUUID(service)); err != nil {
		t.Fatal(err)
	}
	if err := NewOperationStore(p).MarkModelMaterializing(ctx, inferencebiz.OperationContext{TenantID: tenant.String(), ServiceID: service.String(), TargetGeneration: item.Generation, LeaseToken: item.LeaseToken}); err != nil {
		t.Fatalf("MarkModelMaterializing() error=%v", err)
	}
	runtime, err = New(p).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
	if err != nil || runtime.ModelReady {
		t.Fatalf("model-ready reset runtime=%+v err=%v", runtime, err)
	}
	if runtime.ModelReadyKnown {
		t.Fatalf("model-ready reset left known fact true: %+v", runtime)
	}
	if err := NewOperationStore(p).SaveModelObservation(ctx, inferencebiz.OperationContext{TenantID: tenant.String(), ServiceID: service.String(), TargetGeneration: item.Generation, LeaseToken: item.LeaseToken}, inferencebiz.ModelObservation{Known: true, Ready: false, Reason: "artifact not ready"}); err != nil {
		t.Fatalf("SaveModelObservation() error=%v", err)
	}
	runtime, err = New(p).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
	if err != nil || !runtime.ModelReadyKnown || runtime.ModelReady {
		t.Fatalf("known model-not-ready fact not persisted: %+v err=%v", runtime, err)
	}
	var eventCount int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM inference_observation_events WHERE tenant_id=$1 AND service_id=$2 AND generation=$3`, pgUUID(tenant), pgUUID(service), item.Generation).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("observation history rows=%d, want 1", eventCount)
	}
	var sourceKind, sourceUID, sourceResourceVersion string
	if err := p.QueryRow(ctx, `SELECT source_kind, source_uid, source_resource_version FROM inference_observation_events WHERE tenant_id=$1 AND service_id=$2 AND generation=$3`, pgUUID(tenant), pgUUID(service), item.Generation).Scan(&sourceKind, &sourceUID, &sourceResourceVersion); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "Deployment" || sourceUID != "runtime-uid" || sourceResourceVersion != "7" {
		t.Fatalf("observation source=(%q,%q,%q), want Deployment/runtime-uid/7", sourceKind, sourceUID, sourceResourceVersion)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := reconcileStore.SaveObservationForWork(ctx, item, bizreconcile.Observation{Generation: item.Generation}); !errors.Is(err, bizreconcile.ErrStaleGeneration) {
		t.Fatalf("expired lease error=%v, want stale generation", err)
	}
	if err := p.QueryRow(ctx, `SELECT count(*) FROM inference_observation_events WHERE tenant_id=$1 AND service_id=$2 AND generation=$3`, pgUUID(tenant), pgUUID(service), item.Generation).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("stale observation changed immutable history rows=%d, want 1", eventCount)
	}

}
