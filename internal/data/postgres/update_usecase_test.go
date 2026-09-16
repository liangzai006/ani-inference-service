package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

func TestUpdateUseCaseClonesSpecWithGenerationCAS(t *testing.T) {
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
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: service.String(), OperationID: createOp.String(), Name: "update-" + service.String(), ModelVersionID: uuid.New().String(), RequestHash: "create-update", IdempotencyKey: "create-update-" + createOp.String()}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_operations SET phase='succeeded', step='complete', completed_at=now() WHERE tenant_id=$1 AND id=$2", tenant, createOp); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_quota_reservations SET state='confirmed' WHERE tenant_id=$1 AND operation_id=$2", tenant, createOp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1 AND id=$2",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1 AND operation_id IN (SELECT id FROM inference_operations WHERE tenant_id=$1 AND service_id=$2)",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_publications WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1 AND service_id=$2",
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
	uc := NewUpdateUseCase(NewRepository(pool))
	out, err := uc.Update(ctx, inference.UpdateInput{TenantID: tenant.String(), ServiceID: service.String(), RequestID: "update-1", RequestHash: "update-hash", ExpectedGeneration: 1, Resources: resources.Normalized{Requests: map[string]string{"cpu": "2"}, Limits: map[string]string{"memory": "8Gi"}}, Replicas: 1, WorkerReplicas: 1, RuntimeMode: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetOperation().GetTargetGeneration() != 2 {
		t.Fatalf("response=%v", out)
	}
	row, err := New(pool).GetService(ctx, GetServiceParams{TenantID: pgUUID(tenant), ID: pgUUID(service)})
	if err != nil {
		t.Fatal(err)
	}
	if row.DesiredGeneration != 2 {
		t.Fatalf("desired generation=%d", row.DesiredGeneration)
	}
	_, err = New(pool).GetSpec(ctx, GetSpecParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := New(pool).GetPreviousQuotaReservation(ctx, GetPreviousQuotaReservationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	if previous.Generation != 1 || previous.ServiceID != pgUUID(service) {
		t.Fatalf("previous reservation=%+v, want generation 1 for update target", previous)
	}
	current, err := New(pool).GetActiveQuotaReservation(ctx, GetActiveQuotaReservationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != 2 || current.State != "pending" {
		t.Fatalf("target reservation=%+v, want pending generation 2", current)
	}
	_, err = uc.Update(ctx, inference.UpdateInput{TenantID: tenant.String(), ServiceID: service.String(), RequestID: "update-stale", RequestHash: "stale", ExpectedGeneration: 1, Resources: resources.Normalized{}, Replicas: 1, WorkerReplicas: 1, RuntimeMode: "deployment"})
	if !errors.Is(err, inference.ErrGenerationConflict) {
		t.Fatalf("stale update err=%v", err)
	}
}
