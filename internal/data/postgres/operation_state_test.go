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
)

func TestOperationStepCASIntegration(t *testing.T) {
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
	tenant, otherTenant, service, operation := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, tid := range []uuid.UUID{tenant, otherTenant} {
		sid := service
		if tid == otherTenant {
			sid = uuid.New()
		}
		if err := New(p).InsertService(ctx, InsertServiceParams{TenantID: pgUUID(tid), ID: pgUUID(sid), Name: "operation-" + tid.String(), DesiredState: "running", DesiredGeneration: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := New(p).InsertOperation(ctx, InsertOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(operation), ServiceID: pgUUID(service), Kind: "create", Phase: "pending", Step: "admission", TargetGeneration: 1, RequestHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).SetCurrentOperation(ctx, SetCurrentOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(service), CurrentOperationID: pgUUID(operation)}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), DirtyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	workStore := NewWorkStore(p, WorkStoreOptions{Owner: "operation-cas-test", LeaseSeconds: 10})
	item, ok, err := workStore.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("claim durable work item: item=%+v ok=%v err=%v", item, ok, err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1 AND id=$2",
			"DELETE FROM inference_operations WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2",
		} {
			if _, err := p.Exec(context.Background(), statement, pgUUID(tenant), pgUUID(service)); err != nil {
				t.Errorf("cleanup tenant %s %q: %v", tenant, statement, err)
			}
		}
		if _, err := p.Exec(context.Background(), "DELETE FROM inference_services WHERE tenant_id=$1", pgUUID(otherTenant)); err != nil {
			t.Errorf("cleanup tenant %s: %v", otherTenant, err)
		}
	})
	r := NewRepository(p)
	advance := OperationStepInput{TenantID: tenant.String(), OperationID: operation.String(), Kind: "create", LeaseToken: item.LeaseToken, TargetGeneration: 1, ExpectedPhase: inferencebiz.OperationPending, ExpectedStep: "admission", NextPhase: inferencebiz.OperationRunning, NextStep: "admission"}
	if err := r.AdvanceOperationStepCAS(ctx, advance); err != nil {
		t.Fatalf("claim operation: %v", err)
	}
	advance.ExpectedPhase, advance.ExpectedStep, advance.NextStep = inferencebiz.OperationRunning, "admission", "reserve_quota"
	if err := r.AdvanceOperationStepCAS(ctx, advance); err != nil {
		t.Fatalf("advance operation: %v", err)
	}
	if err := r.AdvanceOperationStepCAS(ctx, advance); !errors.Is(err, ErrOperationCAS) {
		t.Fatalf("stale step accepted: %v", err)
	}
	if err := r.RetryOperationStepCAS(ctx, OperationStepInput{TenantID: tenant.String(), OperationID: operation.String(), Kind: "create", LeaseToken: item.LeaseToken, TargetGeneration: 1, ExpectedPhase: inferencebiz.OperationRunning, ExpectedStep: "reserve_quota", ErrorCode: "quota_unavailable", ErrorMessage: "quota provider unavailable"}, time.Minute); err != nil {
		t.Fatalf("retry operation: %v", err)
	}
	op, err := New(p).GetOperation(ctx, GetOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(operation)})
	if err != nil {
		t.Fatal(err)
	}
	if op.Phase != "pending" || op.Attempt != 1 || op.ErrorCode != "quota_unavailable" {
		t.Fatalf("retry state not durable: %+v", op)
	}
	if err := r.AdvanceOperationStepCAS(ctx, OperationStepInput{TenantID: otherTenant.String(), OperationID: operation.String(), Kind: "create", LeaseToken: item.LeaseToken, TargetGeneration: 1, ExpectedPhase: inferencebiz.OperationPending, ExpectedStep: "reserve_quota", NextPhase: inferencebiz.OperationRunning, NextStep: "reserve_quota"}); !errors.Is(err, ErrOperationCAS) {
		t.Fatalf("cross-tenant operation update accepted: %v", err)
	}
}
