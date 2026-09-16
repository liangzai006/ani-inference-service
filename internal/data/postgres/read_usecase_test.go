package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadUseCaseListKeysetIsTenantScoped(t *testing.T) {
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
	tenant := uuid.New()
	otherTenant := uuid.New()
	create := NewRepository(pool)
	for i := 0; i < 3; i++ {
		serviceID, opID := uuid.New(), uuid.New()
		if _, err := create.CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: serviceID.String(), OperationID: opID.String(), Name: "list-" + serviceID.String(), ModelVersionID: uuid.New().String(), RequestHash: RequestHash([]byte(opID.String())), IdempotencyKey: "list-" + opID.String()}); err != nil {
			t.Fatal(err)
		}
	}
	otherService, otherOp := uuid.New(), uuid.New()
	if _, err := create.CreateService(ctx, CreateAggregateInput{TenantID: otherTenant.String(), ServiceID: otherService.String(), OperationID: otherOp.String(), Name: "other-" + otherService.String(), ModelVersionID: uuid.New().String(), RequestHash: RequestHash([]byte(otherOp.String())), IdempotencyKey: "other-" + otherOp.String()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{tenant, otherTenant} {
			for _, query := range []string{
				"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
				"DELETE FROM inference_audit_events WHERE tenant_id=$1",
				"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
				"DELETE FROM inference_quota_reservations WHERE tenant_id=$1",
				"DELETE FROM inference_resource_work WHERE tenant_id=$1",
				"DELETE FROM inference_observation_events WHERE tenant_id=$1",
				"DELETE FROM inference_publications WHERE tenant_id=$1",
				"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
				"DELETE FROM inference_runtime WHERE tenant_id=$1",
				"DELETE FROM inference_specs WHERE tenant_id=$1",
				"DELETE FROM inference_operations WHERE tenant_id=$1",
				"DELETE FROM inference_services WHERE tenant_id=$1",
			} {
				if _, err := pool.Exec(context.Background(), query, id); err != nil {
					t.Errorf("cleanup tenant %s %q: %v", id, query, err)
				}
			}
		}
	})
	read := NewReadUseCase(pool)
	first, token, err := read.ListServices(ctx, tenant.String(), 1, "")
	if err != nil || len(first) != 1 || token == "" {
		t.Fatalf("first page=%v token=%q err=%v", first, token, err)
	}
	second, next, err := read.ListServices(ctx, tenant.String(), 1, token)
	if err != nil || len(second) != 1 || second[0].GetId() == first[0].GetId() || next == "" {
		t.Fatalf("second page=%v next=%q err=%v", second, next, err)
	}
	third, final, err := read.ListServices(ctx, tenant.String(), 1, next)
	if err != nil || len(third) != 1 || final != "" {
		t.Fatalf("third page=%v final=%q err=%v", third, final, err)
	}
	other, _, err := read.ListServices(ctx, otherTenant.String(), 10, "")
	if err != nil || len(other) != 1 || other[0].GetId() == first[0].GetId() {
		t.Fatalf("cross-tenant list leaked rows: %v err=%v", other, err)
	}
}

func TestReadUseCaseProjectsArtifactAndEngineFields(t *testing.T) {
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
	tenant, serviceID, opID := uuid.New(), uuid.New(), uuid.New()
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: serviceID.String(), OperationID: opID.String(),
		Name: "projection-" + serviceID.String(), ModelVersionID: uuid.New().String(),
		ArtifactProvider: "s3", ArtifactRef: "bucket/model", ArtifactSHA256: "abc123",
		ImageRef: "engine:v2", ServedModelName: "resnet", EngineRuntime: "vllm",
		CommandArgv: []byte(`["--port","8080"]`), RequestHash: RequestHash([]byte(opID.String())),
		IdempotencyKey: "projection-" + opID.String(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1", "DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1", "DELETE FROM inference_resource_work WHERE tenant_id=$1",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1", "DELETE FROM inference_publications WHERE tenant_id=$1",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1", "DELETE FROM inference_runtime WHERE tenant_id=$1",
			"DELETE FROM inference_specs WHERE tenant_id=$1", "DELETE FROM inference_operations WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := pool.Exec(context.Background(), query, tenant); err != nil {
				t.Errorf("cleanup %q: %v", query, err)
			}
		}
	})
	read := NewReadUseCase(pool)
	got, err := read.GetService(ctx, tenant.String(), serviceID.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetAppliedGeneration() != 0 || got.GetServedModelName() != "resnet" || got.GetModelArtifact().GetProvider() != "s3" || got.GetModelArtifact().GetReference() != "bucket/model" || got.GetEngine().GetImage() != "engine:v2" || got.GetEngine().GetType() != "vllm" || len(got.GetEngine().GetCommand()) != 2 {
		t.Fatalf("projection lost artifact/engine fields: %+v", got)
	}
}

func TestReadUseCaseListsOperationsTenantScoped(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "ops-" + service.String(),
		ModelVersionID: uuid.NewString(), RequestHash: "ops", IdempotencyKey: "ops", Replicas: 1,
	}); err != nil {
		t.Fatal(err)
	}
	read := NewReadUseCase(pool)
	got, next, err := read.ListOperations(ctx, tenant.String(), service.String(), 10, "")
	if err != nil || len(got) != 1 || got[0].GetServiceId() != service.String() || next != "" {
		t.Fatalf("operations=%v next=%q err=%v", got, next, err)
	}
	all, _, err := read.ListOperations(ctx, tenant.String(), "", 10, "")
	if err != nil || len(all) != 1 || all[0].GetServiceId() != service.String() {
		t.Fatalf("tenant operation list leaked or missing: %v err=%v", all, err)
	}
}
