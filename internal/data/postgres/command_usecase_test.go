package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

func TestCommandUseCaseAcceptsFencedStopAndReplays(t *testing.T) {
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
	tenant, service, createOp := uuid.New(), uuid.New(), uuid.New()
	_, err = NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), OperationID: createOp.String(),
		Name: "command-" + service.String(), ModelVersionID: uuid.New().String(),
		RequestHash: RequestHash([]byte("command-create")), IdempotencyKey: "create-" + createOp.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_operations SET phase='succeeded', step='complete', completed_at=now() WHERE tenant_id=$1 AND id=$2", tenant, createOp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1 AND id=$2",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1 AND operation_id IN (SELECT id FROM inference_operations WHERE tenant_id=$1 AND service_id=$2)",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_runtime WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_specs WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_operations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2",
		} {
			if _, err := pool.Exec(context.Background(), query, tenant, service); err != nil {
				t.Errorf("cleanup %q: %v", query, err)
			}
		}
	})
	uc := NewCommandUseCase(NewRepository(pool))
	in := inference.CommandInput{TenantID: tenant.String(), ServiceID: service.String(), Kind: "stop", RequestID: "stop-1", RequestHash: "hash-stop", ExpectedGeneration: 1}
	out, err := uc.Command(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.GetOperation().GetKind() != "stop" || out.GetOperation().GetTargetGeneration() != 2 {
		t.Fatalf("unexpected command response: %v", out)
	}
	replay, err := uc.Command(ctx, in)
	if err != nil || replay.GetOperation().GetId() != out.GetOperation().GetId() {
		t.Fatalf("replay=%v err=%v", replay, err)
	}
	_, err = uc.Command(ctx, inference.CommandInput{TenantID: tenant.String(), ServiceID: service.String(), Kind: "start", RequestID: "start-stale", RequestHash: "hash-start", ExpectedGeneration: 1})
	if err == nil || !errors.Is(err, inference.ErrGenerationConflict) {
		t.Fatalf("stale command err=%v", err)
	}
}

func TestCommandUseCaseDeleteCancelsActiveOperation(t *testing.T) {
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
	tenant, service, createOp := uuid.New(), uuid.New(), uuid.New()
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), OperationID: createOp.String(),
		Name: "delete-active-" + service.String(), ModelVersionID: uuid.New().String(),
		RequestHash: RequestHash([]byte("delete-active-create")), IdempotencyKey: "delete-active-" + createOp.String(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1 AND id=$2",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1 AND operation_id IN (SELECT id FROM inference_operations WHERE tenant_id=$1 AND service_id=$2)",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_publications WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_runtime WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_specs WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_operations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2",
		} {
			if _, err := pool.Exec(context.Background(), query, tenant, service); err != nil {
				t.Errorf("cleanup %q: %v", query, err)
			}
		}
	})

	uc := NewCommandUseCase(NewRepository(pool))
	deleted, err := uc.Command(ctx, inference.CommandInput{
		TenantID: tenant.String(), ServiceID: service.String(), Kind: "delete",
		RequestID: "delete-active-1", RequestHash: "delete-active-hash", ExpectedGeneration: 1,
	})
	if err != nil {
		t.Fatalf("delete with active operation: %v", err)
	}
	if deleted.GetOperation().GetKind() != "delete" || deleted.GetOperation().GetTargetGeneration() != 2 {
		t.Fatalf("unexpected delete response: %v", deleted)
	}
	old, err := New(pool).GetOperation(ctx, GetOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(createOp)})
	if err != nil {
		t.Fatal(err)
	}
	if old.Phase != "failed" || old.ErrorCode != "cancelled_by_delete" || old.CompletedAt.Time.IsZero() {
		t.Fatalf("old operation was not cancelled: phase=%q code=%q completed=%v", old.Phase, old.ErrorCode, old.CompletedAt)
	}
	workRow, err := New(pool).GetResourceWork(ctx, GetResourceWorkParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
	if err != nil {
		t.Fatal(err)
	}
	if workRow.LeaseToken.Valid || workRow.LeaseUntil.Valid {
		t.Fatalf("cancelled operation still owns work lease: %+v", workRow)
	}
}
