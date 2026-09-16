package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

func TestSaveObservationInvocationHealthPreservesOnlySameGenerationFact(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	createObservationAggregate(t, ctx, pool, tenant, service)
	store := NewReconcileStore(pool)

	save := func(health string, generation int64) {
		t.Helper()
		if err := store.SaveObservation(ctx, tenant.String(), service.String(), generation, bizreconcile.Observation{
			Generation: generation, RuntimePhase: "ready", PublicationPhase: "withdrawn", InvocationHealth: health,
		}); err != nil {
			t.Fatalf("SaveObservation(%q, generation=%d): %v", health, generation, err)
		}
	}
	assertHealth := func(want string) {
		t.Helper()
		got, err := New(pool).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
		if err != nil {
			t.Fatal(err)
		}
		if got.InvocationHealth != want {
			t.Fatalf("invocation health=%q, want %q", got.InvocationHealth, want)
		}
	}

	save("healthy", 1)
	assertHealth("healthy")
	save("unknown", 1)
	assertHealth("unknown")
	save("unhealthy", 1)
	assertHealth("unhealthy")
	save("", 1)
	assertHealth("unhealthy")

	if _, err := pool.Exec(ctx, "UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2", pgUUID(tenant), pgUUID(service)); err != nil {
		t.Fatal(err)
	}
	save("", 2)
	assertHealth("unknown")
}

func TestSaveObservationForWorkInvocationHealthPreservesOnlySameGenerationFact(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	createObservationAggregate(t, ctx, pool, tenant, service)
	store := NewWorkStore(pool, WorkStoreOptions{Owner: "invocation-observation", LeaseSeconds: 30})
	item, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("Claim() item=%+v ok=%v err=%v", item, ok, err)
	}
	reconcile := NewReconcileStore(pool)
	save := func(health string, current work.Item) {
		t.Helper()
		if err := reconcile.SaveObservationForWork(ctx, current, bizreconcile.Observation{
			Generation: current.Generation, RuntimePhase: "ready", PublicationPhase: "withdrawn", InvocationHealth: health,
		}); err != nil {
			t.Fatalf("SaveObservationForWork(%q, generation=%d): %v", health, current.Generation, err)
		}
	}
	assertHealth := func(want string) {
		t.Helper()
		got, err := New(pool).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
		if err != nil {
			t.Fatal(err)
		}
		if got.InvocationHealth != want {
			t.Fatalf("invocation health=%q, want %q", got.InvocationHealth, want)
		}
	}

	save("healthy", item)
	assertHealth("healthy")
	save("unknown", item)
	assertHealth("unknown")
	save("unhealthy", item)
	assertHealth("unhealthy")
	save("", item)
	assertHealth("unhealthy")
	if err := store.Commit(ctx, item, work.Result{Generation: item.Generation}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2", pgUUID(tenant), pgUUID(service)); err != nil {
		t.Fatal(err)
	}
	if err := store.Notify(ctx, tenant.String(), service.String(), 2); err != nil {
		t.Fatal(err)
	}
	item, ok, err = store.Claim(ctx, tenant.String())
	if err != nil || !ok || item.Generation != 2 {
		t.Fatalf("Claim() generation-2 item=%+v ok=%v err=%v", item, ok, err)
	}
	save("", item)
	assertHealth("unknown")
}

func TestSaveObservationInvocationHealthIsTenantScoped(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	createObservationAggregate(t, ctx, pool, tenant, service)
	otherTenant := uuid.New()
	createObservationAggregate(t, ctx, pool, otherTenant, service)
	t.Cleanup(func() { cleanupObservationTenant(t, pool, otherTenant) })

	store := NewReconcileStore(pool)
	for _, tenantID := range []uuid.UUID{tenant, otherTenant} {
		if err := store.SaveObservation(ctx, tenantID.String(), service.String(), 1, bizreconcile.Observation{
			Generation: 1, RuntimePhase: "ready", PublicationPhase: "withdrawn", InvocationHealth: "healthy",
		}); err != nil {
			t.Fatalf("seed tenant %s: %v", tenantID, err)
		}
	}
	if err := store.SaveObservation(ctx, tenant.String(), service.String(), 1, bizreconcile.Observation{
		Generation: 1, RuntimePhase: "ready", PublicationPhase: "withdrawn", InvocationHealth: "unknown",
	}); err != nil {
		t.Fatal(err)
	}
	for tenantID, want := range map[uuid.UUID]string{tenant: "unknown", otherTenant: "healthy"} {
		got, err := New(pool).GetRuntime(ctx, GetRuntimeParams{TenantID: pgUUID(tenantID), ServiceID: pgUUID(service)})
		if err != nil {
			t.Fatal(err)
		}
		if got.InvocationHealth != want {
			t.Fatalf("tenant %s invocation health=%q, want %q", tenantID, got.InvocationHealth, want)
		}
	}
}

func createObservationAggregate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, service uuid.UUID) {
	t.Helper()
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "invocation-observation-" + service.String(),
		ModelVersionID: uuid.New().String(), RequestHash: "invocation-observation-" + service.String(),
		IdempotencyKey: "invocation-observation-" + service.String(),
	}); err != nil {
		t.Fatal(err)
	}
}

func cleanupObservationTenant(t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, statement := range []string{
		"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
		"DELETE FROM inference_audit_events WHERE tenant_id=$1",
		"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
		"DELETE FROM inference_quota_reservations WHERE tenant_id=$1",
		"DELETE FROM inference_publications WHERE tenant_id=$1",
		"DELETE FROM inference_observation_events WHERE tenant_id=$1",
		"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
		"DELETE FROM inference_resource_work WHERE tenant_id=$1",
		"DELETE FROM inference_runtime WHERE tenant_id=$1",
		"DELETE FROM inference_specs WHERE tenant_id=$1",
		"DELETE FROM inference_operations WHERE tenant_id=$1",
		"DELETE FROM inference_services WHERE tenant_id=$1",
	} {
		if _, err := pool.Exec(cleanupCtx, statement, tenant); err != nil {
			t.Errorf("cleanup tenant %s (%q): %v", tenant, statement, err)
		}
	}
}
