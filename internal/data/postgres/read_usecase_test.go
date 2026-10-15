package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
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
		RuntimeProvider: "kserve",
		EngineJSON:      []byte(`{"command":["/bin/sh","-c"],"args":["--port 8080"]}`), RequestHash: RequestHash([]byte(opID.String())),
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
	if got.GetAppliedGeneration() != 0 || got.GetServedModelName() != "resnet" || got.GetRuntime().GetProvider() != "kserve" || got.GetModelArtifact().GetProvider() != "s3" || got.GetModelArtifact().GetReference() != "bucket/model" || got.GetEngine().GetImage() != "engine:v2" || got.GetEngine().GetType() != "vllm" || got.GetEngine().GetCommand() != "--port 8080" {
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

func TestReadUseCasePageQueriesKeepCountAndRowsScoped(t *testing.T) {
	ctx, pool, tenant, _ := lifecycleDatabase(t)
	otherTenant := uuid.New()
	cleanupOther := func() {
		for _, query := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1",
			"DELETE FROM inference_operations WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := pool.Exec(context.Background(), query, otherTenant); err != nil {
				t.Errorf("cleanup other tenant: %v", err)
			}
		}
	}
	t.Cleanup(cleanupOther)

	create := func(tid, sid uuid.UUID) {
		opID := uuid.New()
		if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
			TenantID: tid.String(), ServiceID: sid.String(), OperationID: opID.String(),
			Name: "page-" + sid.String(), ModelVersionID: uuid.NewString(),
			RequestHash: RequestHash([]byte(opID.String())), IdempotencyKey: opID.String(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	services := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, sid := range services {
		create(tenant, sid)
	}
	create(otherTenant, uuid.New())

	read := NewReadUseCase(pool)
	first, total, err := read.ListServicesPage(ctx, tenant.String(), inferencebiz.ListOptions{Limit: 2})
	if err != nil || total != 3 || len(first) != 2 {
		t.Fatalf("first page=%v total=%d err=%v", first, total, err)
	}
	second, secondTotal, err := read.ListServicesPage(ctx, tenant.String(), inferencebiz.ListOptions{Limit: 2, Offset: 2})
	if err != nil || secondTotal != 3 || len(second) != 1 {
		t.Fatalf("second page=%v total=%d err=%v", second, secondTotal, err)
	}
	seen := map[string]bool{}
	for _, item := range append(first, second...) {
		if seen[item.GetId()] {
			t.Fatalf("duplicate service across pages: %s", item.GetId())
		}
		seen[item.GetId()] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d tenant services, want 3", len(seen))
	}

	ops, opsTotal, err := read.ListOperationsPage(ctx, tenant.String(), services[0].String(), inferencebiz.ListOptions{Limit: 10})
	if err != nil || opsTotal != 1 || len(ops) != 1 || ops[0].GetServiceId() != services[0].String() {
		t.Fatalf("resource-scoped operations=%v total=%d err=%v", ops, opsTotal, err)
	}
}
