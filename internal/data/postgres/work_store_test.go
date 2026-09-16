package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

func TestWorkStoreLeaseAndObservationIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	tenantPG, _ := workUUID("tenant", tenant.String())
	servicePG, _ := workUUID("service", service.String())
	otherTenant, otherService := uuid.New(), uuid.New()
	otherTenantPG, _ := workUUID("tenant", otherTenant.String())
	otherServicePG, _ := workUUID("service", otherService.String())
	q := New(pool)
	if err := q.InsertService(ctx, InsertServiceParams{TenantID: tenantPG, ID: servicePG, Name: "work-" + service.String(), DesiredState: "running", DesiredGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenantPG, ServiceID: servicePG, DirtyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertService(ctx, InsertServiceParams{TenantID: otherTenantPG, ID: otherServicePG, Name: "work-" + otherService.String(), DesiredState: "running", DesiredGeneration: 7}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: otherTenantPG, ServiceID: otherServicePG, DirtyVersion: 7}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, tid := range []uuid.UUID{tenant, otherTenant} {
			for _, statement := range []string{
				"DELETE FROM inference_resource_work WHERE tenant_id=$1",
				"DELETE FROM inference_services WHERE tenant_id=$1",
			} {
				if _, err := pool.Exec(context.Background(), statement, tid); err != nil {
					t.Errorf("cleanup work fixture: %v", err)
				}
			}
		}
	})
	store := NewWorkStore(pool, WorkStoreOptions{Owner: "test-worker", LeaseSeconds: .05, ObserveSeconds: .05, RetrySeconds: .01})
	services, err := store.ScanUndeletedServices(ctx, tenant.String())
	if err != nil || len(services) != 1 || services[0].ServiceID != service.String() || services[0].Generation != 1 {
		t.Fatalf("startup service scan: services=%+v err=%v", services, err)
	}
	due, err := store.ScanDue(ctx, tenant.String())
	if err != nil || len(due) != 1 || due[0].ServiceID != service.String() || due[0].Generation != 1 {
		t.Fatalf("due scan: items=%+v err=%v", due, err)
	}
	first, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("first claim: item=%+v ok=%v err=%v", first, ok, err)
	}
	if _, ok, err := store.Claim(ctx, tenant.String()); err != nil || ok {
		t.Fatalf("claim before expiry: ok=%v err=%v", ok, err)
	}
	time.Sleep(70 * time.Millisecond)
	if err := store.Commit(ctx, first, work.Result{Generation: first.Generation}); !errors.Is(err, work.ErrLeaseLost) {
		t.Fatalf("expired commit err=%v", err)
	}
	second, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("second claim: item=%+v ok=%v err=%v", second, ok, err)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatal("lease token was reused")
	}
	if err := store.Commit(ctx, first, work.Result{Generation: first.Generation}); !errors.Is(err, work.ErrLeaseLost) {
		t.Fatalf("old token commit err=%v", err)
	}
	if err := store.Commit(ctx, second, work.Result{Generation: second.Generation}); err != nil {
		t.Fatalf("current commit: %v", err)
	}
	time.Sleep(70 * time.Millisecond)
	third, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("periodic observation claim: item=%+v ok=%v err=%v", third, ok, err)
	}
	if third.Generation != 1 || third.DirtyVersion != 1 {
		t.Fatalf("unexpected periodic item: %+v", third)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2", tenantPG, servicePG); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(ctx, third, errors.New("generation changed")); !errors.Is(err, work.ErrLeaseLost) {
		t.Fatalf("generation CAS err=%v", err)
	}
}

func TestWorkStoreSameGenerationNotificationSurvivesAcknowledgement(t *testing.T) {
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
	if err := p.Ping(ctx); err != nil {
		p.Close()
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	tenantPG, _ := workUUID("tenant", tenant.String())
	servicePG, _ := workUUID("service", service.String())
	cleanup := func() {
		cleanupCtx := context.Background()
		if _, err := p.Exec(cleanupCtx, "DELETE FROM inference_resource_work WHERE tenant_id=$1 AND service_id=$2", tenantPG, servicePG); err != nil {
			t.Errorf("cleanup resource_work: %v", err)
		}
		if _, err := p.Exec(cleanupCtx, "DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2", tenantPG, servicePG); err != nil {
			t.Errorf("cleanup service: %v", err)
		}
		p.Close()
	}
	t.Cleanup(cleanup)
	q := New(p)
	if err := q.InsertService(ctx, InsertServiceParams{TenantID: tenantPG, ID: servicePG, Name: "notify-" + service.String(), DesiredState: "running", DesiredGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenantPG, ServiceID: servicePG, DirtyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(p, WorkStoreOptions{Owner: "notify-test", LeaseSeconds: 10, ObserveSeconds: 60, RetrySeconds: .01})

	first, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("first claim: item=%+v ok=%v err=%v", first, ok, err)
	}
	// A status event for the same generation arrives while the worker owns the
	// lease. It must create a new notification sequence, not be hidden by the
	// acknowledgement of first.
	if err := store.Notify(ctx, tenant.String(), service.String(), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, first, work.Result{Generation: first.Generation}); err != nil {
		t.Fatal(err)
	}
	second, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("same-generation notification was acknowledged away: item=%+v ok=%v err=%v", second, ok, err)
	}
	if second.DirtyVersion <= first.DirtyVersion {
		t.Fatalf("dirty notification sequence did not advance: first=%d second=%d", first.DirtyVersion, second.DirtyVersion)
	}
	if err := store.Commit(ctx, second, work.Result{Generation: second.Generation}); err != nil {
		t.Fatal(err)
	}
	// With no new notification, the successful acknowledgement schedules the
	// normal observation interval instead of a hot loop.
	third, ok, err := store.Claim(ctx, tenant.String())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("claim unexpectedly became immediately due without a notification: %+v", third)
	}
	if err := store.Notify(ctx, tenant.String(), service.String(), 1); err != nil {
		t.Fatal(err)
	}
	third, ok, err = store.Claim(ctx, tenant.String())
	if err != nil || !ok || third.DirtyVersion <= second.DirtyVersion {
		t.Fatalf("idle same-generation notification did not wake: item=%+v ok=%v err=%v", third, ok, err)
	}
	if err := store.Commit(ctx, third, work.Result{Generation: third.Generation}); err != nil {
		t.Fatal(err)
	}
	// Older runtime events are still wake-ups, but cannot lower the durable
	// sequence or business generation returned by Claim.
	if err := store.Notify(ctx, tenant.String(), service.String(), 0); err != nil {
		t.Fatal(err)
	}
	fourth, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok || fourth.Generation != 1 || fourth.DirtyVersion <= third.DirtyVersion {
		t.Fatalf("older-generation notification did not wake safely: item=%+v ok=%v err=%v", fourth, ok, err)
	}
}

func TestWorkStoreRecoverTenantRecreatesMissingWorkIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	tenantPG, _ := workUUID("tenant", tenant.String())
	servicePG, _ := workUUID("service", service.String())
	q := New(pool)
	if err := q.InsertService(ctx, InsertServiceParams{
		TenantID: tenantPG, ID: servicePG, Name: "recover-" + service.String(),
		DesiredState: "running", DesiredGeneration: 3,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM inference_resource_work WHERE tenant_id=$1", tenantPG); err != nil {
			t.Errorf("cleanup work: %v", err)
		}
		if _, err := pool.Exec(context.Background(), "DELETE FROM inference_services WHERE tenant_id=$1", tenantPG); err != nil {
			t.Errorf("cleanup service: %v", err)
		}
	})

	store := NewWorkStore(pool, WorkStoreOptions{Owner: "recover-test", LeaseSeconds: 1})
	// Exercise the same tenant-scoped repair used by Recover, without waking
	// unrelated services in the shared developer database.
	if err := store.recoverTenant(ctx, tenant.String()); err != nil {
		t.Fatalf("recoverTenant() error = %v", err)
	}
	item, ok, err := store.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("claim recovered work: item=%+v ok=%v err=%v", item, ok, err)
	}
	if item.ServiceID != service.String() || item.Generation != 3 || item.DirtyVersion != 3 {
		t.Fatalf("unexpected recovered work item: %+v", item)
	}
}

func TestWorkStoreRepairDoesNotWakeExistingWorkIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	tenantPG, _ := workUUID("tenant", tenant.String())
	servicePG, _ := workUUID("service", service.String())
	q := New(pool)
	if err := q.InsertService(ctx, InsertServiceParams{
		TenantID: tenantPG, ID: servicePG, Name: "repair-" + service.String(),
		DesiredState: "running", DesiredGeneration: 4,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM inference_resource_work WHERE tenant_id=$1", tenantPG); err != nil {
			t.Errorf("cleanup work: %v", err)
		}
		if _, err := pool.Exec(context.Background(), "DELETE FROM inference_services WHERE tenant_id=$1", tenantPG); err != nil {
			t.Errorf("cleanup service: %v", err)
		}
	})
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenantPG, ServiceID: servicePG, DirtyVersion: 9}); err != nil {
		t.Fatal(err)
	}
	store := NewWorkStore(pool, WorkStoreOptions{Owner: "repair-test", LeaseSeconds: 1})
	if err := store.Repair(ctx); err != nil {
		t.Fatalf("Repair() error = %v", err)
	}
	row, err := q.GetResourceWork(ctx, GetResourceWorkParams{TenantID: tenantPG, ServiceID: servicePG})
	if err != nil {
		t.Fatal(err)
	}
	if row.DirtyVersion != 9 {
		t.Fatalf("Repair changed dirty_version to %d, want 9", row.DirtyVersion)
	}
}

func TestWorkStoreMetricsSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	tenantPG, _ := workUUID("tenant", tenant.String())
	servicePG, _ := workUUID("service", service.String())
	q := New(pool)
	if err := q.InsertService(ctx, InsertServiceParams{TenantID: tenantPG, ID: servicePG, Name: "metrics-" + service.String(), DesiredState: "running", DesiredGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM inference_resource_work WHERE tenant_id=$1", tenantPG)
		_, _ = pool.Exec(context.Background(), "DELETE FROM inference_services WHERE tenant_id=$1", tenantPG)
	})
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenantPG, ServiceID: servicePG, DirtyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewWorkStore(pool, WorkStoreOptions{}).MetricsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DueWork < 1 || snapshot.OldestWorkAge < 0 {
		t.Fatalf("invalid durable work metrics snapshot: %+v", snapshot)
	}
}
