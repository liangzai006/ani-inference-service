package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/audit"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
)

func TestOperationStoreLoadsLeaseFencedOperation(t *testing.T) {
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
	defer p.Close()
	tenant, service, op := uuid.New(), uuid.New(), uuid.New()
	if err := New(p).InsertService(ctx, InsertServiceParams{TenantID: pgUUID(tenant), ID: pgUUID(service), Name: "op-store-" + service.String(), DesiredState: "running", DesiredGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).InsertOperation(ctx, InsertOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(op), ServiceID: pgUUID(service), Kind: "create", Phase: "pending", Step: "admission", TargetGeneration: 1, RequestHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).SetCurrentOperation(ctx, SetCurrentOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(service), CurrentOperationID: pgUUID(op)}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), DirtyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := New(p).AppendAuditEvent(ctx, AppendAuditEventParams{TenantID: pgUUID(tenant), EventID: pgUUID(uuid.New()), ServiceID: pgUUID(service), OperationID: pgUUID(op), Generation: 1, EventType: "inference.create.accepted", Actor: "workload:caller", RequestID: "request-123", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = p.Exec(context.Background(), "UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1 AND id=$2", pgUUID(tenant), pgUUID(service))
		_, _ = p.Exec(context.Background(), "DELETE FROM inference_audit_events WHERE tenant_id=$1", pgUUID(tenant))
		_, _ = p.Exec(context.Background(), "DELETE FROM inference_operations WHERE tenant_id=$1", pgUUID(tenant))
		_, _ = p.Exec(context.Background(), "DELETE FROM inference_resource_work WHERE tenant_id=$1", pgUUID(tenant))
		_, _ = p.Exec(context.Background(), "DELETE FROM inference_services WHERE tenant_id=$1", pgUUID(tenant))
	}()
	ws := NewWorkStore(p, WorkStoreOptions{Owner: "op-store-test", LeaseSeconds: 10})
	item, ok, err := ws.Claim(ctx, tenant.String())
	if err != nil || !ok {
		t.Fatalf("claim item=%+v ok=%v err=%v", item, ok, err)
	}
	store := NewOperationStore(p)
	if err := New(p).UpsertPublication(ctx, UpsertPublicationParams{
		TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 1,
		DesiredPhase: "published", ObservedPhase: "published", InvocationUrl: "http://runtime",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePublication(ctx, publication.Publication{TenantID: tenant.String(), ServiceID: service.String(), OperationID: op.String(), Generation: 1, LeaseToken: item.LeaseToken, State: "withdrawing"}); err != nil {
		t.Fatalf("persist withdrawal intent: %v", err)
	}
	pub, err := New(p).GetPublication(ctx, GetPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if pub.DesiredPhase != "withdrawing" || pub.ObservedPhase != "published" {
		t.Fatalf("withdrawal intent changed observed state: desired=%q observed=%q", pub.DesiredPhase, pub.ObservedPhase)
	}
	loaded, err := store.CurrentOperation(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != op.String() || loaded.TenantID != tenant.String() || loaded.LeaseToken != item.LeaseToken || loaded.Actor != "workload:caller" || loaded.RequestID != "request-123" {
		t.Fatalf("loaded operation=%+v", loaded)
	}
	if err := store.AdvanceOperationStepCASWithAudit(ctx, inferencebiz.StepTransition{TenantID: tenant.String(), OperationID: op.String(), Kind: "create", LeaseToken: item.LeaseToken, TargetGeneration: 1, ExpectedPhase: inferencebiz.OperationPending, ExpectedStep: "admission", NextPhase: inferencebiz.OperationRunning, NextStep: "admission"}, auditEventForTest(tenant, service, op)); err != nil {
		t.Fatal(err)
	}
	if got, err := New(p).GetOperation(ctx, GetOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(op)}); err != nil || got.Phase != "running" {
		t.Fatalf("operation=%+v err=%v", got, err)
	}
}

func auditEventForTest(tenant, service, op uuid.UUID) audit.Event {
	return audit.Event{TenantID: tenant.String(), EventID: uuid.NewString(), ServiceID: service.String(), OperationID: op.String(), Generation: 1, EventType: "test.transition"}
}

func TestReservationSnapshotRejectsInvalidResources(t *testing.T) {
	for _, payload := range []string{
		`null`,
		`{"requests":{"cpu":"-1"}}`,
		`{"requests":{"cpu":"4"},"limits":{"cpu":"2"}}`,
		`{"limits":{"nvidia.com/gpu":"invalid"}}`,
		`{"cpu":"2"}`,
		`{"requests":{"cpu":2}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			_, err := reservationFromRow(InferenceQuotaReservation{RequestedResources: []byte(payload)}, inferencebiz.OperationContext{})
			if err == nil {
				t.Fatal("invalid durable quota snapshot accepted")
			}
		})
	}
}
