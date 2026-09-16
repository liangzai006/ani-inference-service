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

func TestCreateServiceTransactionIntegration(t *testing.T) {
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
	r := NewRepository(pool)
	tenant, serviceID, opID := uuid.New(), uuid.New(), uuid.New()
	got, err := r.CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: serviceID.String(), OperationID: opID.String(),
		Name: "integration-" + serviceID.String(), ModelVersionID: uuid.New().String(),
		EndpointEnabled: true, EndpointContainerPort: 8080, EndpointServicePort: 80, EndpointTargetPort: "8080", EndpointProtocol: "TCP",
		RequestHash: RequestHash([]byte("create")), IdempotencyKey: "k-" + opID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Replayed || got.ServiceID != serviceID.String() || got.OperationID != opID.String() {
		t.Fatalf("unexpected result: %+v", got)
	}
	spec, err := New(pool).GetSpec(ctx, GetSpecParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(serviceID), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !spec.EndpointContainerPort.Valid || spec.EndpointContainerPort.Int32 != 8080 || !spec.EndpointServicePort.Valid || spec.EndpointServicePort.Int32 != 80 || spec.EndpointTargetPort.String != "8080" || spec.EndpointProtocol.String != "TCP" {
		t.Fatalf("endpoint not persisted: %+v", spec)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
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
			if _, err := pool.Exec(context.Background(), statement, tenant, serviceID); err != nil {
				t.Errorf("cleanup %q: %v", statement, err)
			}
		}
	})
	replay, err := r.CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), Name: "ignored", ModelVersionID: uuid.New().String(),
		RequestHash: RequestHash([]byte("create")), IdempotencyKey: "k-" + opID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.ServiceID != serviceID.String() || replay.OperationID != opID.String() {
		t.Fatalf("unexpected replay: %+v", replay)
	}
}

func TestUpdateObservationCASUsesDesiredGenerationAndPersistsLWSFields(t *testing.T) {
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
		TenantID: tenantPG, ID: servicePG, Name: "observation-" + service.String(),
		DesiredState: "running", DesiredGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertRuntime(ctx, InsertRuntimeParams{
		TenantID: tenantPG, ServiceID: servicePG, Generation: 1, RuntimeMode: "leader_worker_set",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			"DELETE FROM inference_runtime WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2",
		} {
			if _, err := pool.Exec(context.Background(), statement, tenantPG, servicePG); err != nil {
				t.Errorf("cleanup %q: %v", statement, err)
			}
		}
	})

	rows, err := q.UpdateObservationCAS(ctx, UpdateObservationCASParams{
		TenantID: tenantPG, ServiceID: servicePG, Generation: 1,
		RuntimeMode: "leader_worker_set", ReadyGroups: 2, ReadyWorkers: 6,
		LwsUid: "lws-uid-1", RuntimePhase: "ready", ReadyReplicas: 6,
		ModelReady: true, PublicationPhase: "withdrawn", InvocationHealth: "unknown",
		Reason: "ready",
	})
	if err != nil || rows != 1 {
		t.Fatalf("current generation update: rows=%d err=%v", rows, err)
	}
	runtime, err := q.GetRuntime(ctx, GetRuntimeParams{TenantID: tenantPG, ServiceID: servicePG})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.RuntimeMode != "leader_worker_set" || runtime.ReadyGroups != 2 || runtime.ReadyWorkers != 6 || runtime.LwsUid != "lws-uid-1" {
		t.Fatalf("LWS observation was not persisted: %+v", runtime)
	}
	rows, err = q.UpdateObservationCAS(ctx, UpdateObservationCASParams{
		TenantID: tenantPG, ServiceID: servicePG, Generation: 2,
		RuntimeMode: "leader_worker_set", ReadyGroups: 8, ReadyWorkers: 8,
		LwsUid: "future", RuntimePhase: "ready", ReadyReplicas: 8,
		ModelReady: true, PublicationPhase: "withdrawn", InvocationHealth: "unknown", Reason: "future",
	})
	if err != nil || rows != 0 {
		t.Fatalf("future generation update was accepted: rows=%d err=%v", rows, err)
	}

	if _, err := pool.Exec(ctx, "UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2", tenantPG, servicePG); err != nil {
		t.Fatal(err)
	}
	rows, err = q.UpdateObservationCAS(ctx, UpdateObservationCASParams{
		TenantID: tenantPG, ServiceID: servicePG, Generation: 1,
		RuntimeMode: "leader_worker_set", ReadyGroups: 9, ReadyWorkers: 9,
		LwsUid: "stale", RuntimePhase: "degraded", ReadyReplicas: 0,
		PublicationPhase: "withdrawn", InvocationHealth: "unknown", Reason: "stale",
	})
	if err != nil || rows != 0 {
		t.Fatalf("old generation update was accepted: rows=%d err=%v", rows, err)
	}
	rows, err = q.UpdateObservationCAS(ctx, UpdateObservationCASParams{
		TenantID: tenantPG, ServiceID: servicePG, Generation: 2,
		RuntimeMode: "leader_worker_set", ReadyGroups: 3, ReadyWorkers: 9,
		LwsUid: "lws-uid-2", RuntimePhase: "ready", ReadyReplicas: 9,
		ModelReady: true, PublicationPhase: "withdrawn", InvocationHealth: "unknown", Reason: "new generation",
	})
	if err != nil || rows != 1 {
		t.Fatalf("current desired generation update: rows=%d err=%v", rows, err)
	}
}

func TestRuntimeBindingCASRejectsStaleUIDAndResourceVersionIntegration(t *testing.T) {
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

	tenant, otherTenant, service := uuid.New(), uuid.New(), uuid.New()
	q := New(p)
	for _, tid := range []uuid.UUID{tenant, otherTenant} {
		if err := q.InsertService(ctx, InsertServiceParams{
			TenantID: pgUUID(tid), ID: pgUUID(service), Name: "binding-" + tid.String(),
			DesiredState: "running", DesiredGeneration: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cleanup := func(tid uuid.UUID) {
		for _, statement := range []string{
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := p.Exec(context.Background(), statement, pgUUID(tid)); err != nil {
				t.Errorf("cleanup tenant %s %q: %v", tid, statement, err)
			}
		}
	}
	t.Cleanup(func() { cleanup(tenant); cleanup(otherTenant) })
	r := NewRepository(p)
	base := RuntimeBindingInput{
		TenantID: tenant.String(), ServiceID: service.String(), Generation: 1,
		ObjectKind: "Deployment", ObjectNamespace: "inference", ObjectName: "svc",
		ObjectUID: "uid-a", ResourceVersion: "11", Role: "runtime",
	}
	if err := r.UpsertRuntimeBindingCAS(ctx, base); err != nil {
		t.Fatalf("first binding insert: %v", err)
	}
	if err := r.UpsertRuntimeBindingCAS(ctx, base); err != nil {
		t.Fatalf("exact insert replay: %v", err)
	}
	got, err := r.GetRuntimeBinding(ctx, tenant.String(), service.String(), 1, "Deployment", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if got.ObjectUid != "uid-a" || got.ResourceVersion != "11" {
		t.Fatalf("unexpected initial binding: %+v", got)
	}
	control := base
	control.ObjectKind, control.Role = "InferenceService", "control"
	control.ObjectName, control.ObjectUID, control.ResourceVersion = "svc-cr", "cr-uid", "21"
	if err := r.UpsertRuntimeBindingCAS(ctx, control); err != nil {
		t.Fatalf("control CR binding insert: %v", err)
	}
	crBinding, err := r.GetRuntimeBinding(ctx, tenant.String(), service.String(), 1, "InferenceService", "control")
	if err != nil || crBinding.ObjectUid != "cr-uid" {
		t.Fatalf("control CR binding missing: %+v err=%v", crBinding, err)
	}

	updated := base
	updated.ResourceVersion = "12"
	updated.ExpectedUID = "uid-a"
	updated.ExpectedResourceVersion = "11"
	if err := r.UpsertRuntimeBindingCAS(ctx, updated); err != nil {
		t.Fatalf("same UID resourceVersion advance: %v", err)
	}

	staleVersion := updated
	staleVersion.ResourceVersion = "13"
	staleVersion.ExpectedResourceVersion = "11"
	if err := r.UpsertRuntimeBindingCAS(ctx, staleVersion); !errors.Is(err, ErrRuntimeBindingCAS) {
		t.Fatalf("stale resourceVersion accepted: %v", err)
	}
	got, err = r.GetRuntimeBinding(ctx, tenant.String(), service.String(), 1, "Deployment", "runtime")
	if err != nil || got.ResourceVersion != "12" {
		t.Fatalf("stale version changed binding: %+v err=%v", got, err)
	}

	staleUID := updated
	staleUID.ObjectUID = "uid-old"
	staleUID.ResourceVersion = "14"
	staleUID.ExpectedUID = "uid-a"
	staleUID.ExpectedResourceVersion = "12"
	if err := r.UpsertRuntimeBindingCAS(ctx, staleUID); !errors.Is(err, ErrRuntimeBindingCAS) {
		t.Fatalf("same-name replacement UID accepted: %v", err)
	}

	wrongTenant := updated
	wrongTenant.TenantID = otherTenant.String()
	wrongTenant.ExpectedUID = ""
	wrongTenant.ExpectedResourceVersion = ""
	if err := r.UpsertRuntimeBindingCAS(ctx, wrongTenant); err != nil {
		t.Fatalf("tenant-scoped binding insert: %v", err)
	}
	other, err := r.GetRuntimeBinding(ctx, otherTenant.String(), service.String(), 1, "Deployment", "runtime")
	if err != nil || other.ObjectUid != "uid-a" {
		t.Fatalf("tenant binding missing: %+v err=%v", other, err)
	}

	if err := r.DeleteRuntimeBindingCAS(ctx, tenant.String(), service.String(), 1, "Deployment", "runtime", "uid-a", "11"); !errors.Is(err, ErrRuntimeBindingCAS) {
		t.Fatalf("stale delete accepted: %v", err)
	}
	if err := r.DeleteRuntimeBindingCAS(ctx, tenant.String(), service.String(), 1, "Deployment", "runtime", "uid-a", "12"); err != nil {
		t.Fatalf("current binding delete: %v", err)
	}
}

func TestListServicesIntegrationIsTenantScopedAndKeysetPaginated(t *testing.T) {
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
	r := NewRepository(pool)
	tenant, otherTenant := uuid.New(), uuid.New()
	for i := 0; i < 3; i++ {
		id := uuid.New()
		if _, err := r.CreateService(ctx, CreateAggregateInput{
			TenantID: tenant.String(), ServiceID: id.String(), OperationID: uuid.NewString(),
			Name: "list-" + id.String(), ModelVersionID: uuid.NewString(),
			RequestHash: RequestHash([]byte(id.String())), IdempotencyKey: "list-" + id.String(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	otherID := uuid.New()
	if _, err := r.CreateService(ctx, CreateAggregateInput{
		TenantID: otherTenant.String(), ServiceID: otherID.String(), OperationID: uuid.NewString(),
		Name: "other-" + otherID.String(), ModelVersionID: uuid.NewString(),
		RequestHash: RequestHash([]byte(otherID.String())), IdempotencyKey: "other-" + otherID.String(),
	}); err != nil {
		t.Fatal(err)
	}
	cleanup := func(id string) {
		for _, statement := range []string{
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
			"DELETE FROM inference_publications WHERE tenant_id=$1",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1",
			"DELETE FROM inference_runtime WHERE tenant_id=$1",
			"DELETE FROM inference_specs WHERE tenant_id=$1",
			"DELETE FROM inference_operations WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := pool.Exec(context.Background(), statement, id); err != nil {
				t.Errorf("cleanup tenant %s %q: %v", id, statement, err)
			}
		}
	}
	t.Cleanup(func() { cleanup(tenant.String()); cleanup(otherTenant.String()) })

	read := NewReadUseCase(pool)
	first, token, err := read.ListServices(ctx, tenant.String(), 2, "")
	if err != nil || len(first) != 2 || token == "" {
		t.Fatalf("first page: len=%d token=%q err=%v", len(first), token, err)
	}
	second, next, err := read.ListServices(ctx, tenant.String(), 2, token)
	if err != nil || len(second) != 1 || next != "" {
		t.Fatalf("second page: len=%d next=%q err=%v", len(second), next, err)
	}
	if first[0].GetId() == second[0].GetId() || first[1].GetId() == second[0].GetId() {
		t.Fatal("keyset cursor repeated a service")
	}
	if _, _, err := read.ListServices(ctx, otherTenant.String(), 2, token); !errors.Is(err, inferencebiz.ErrInvalidPageToken) {
		t.Fatalf("cross-tenant token accepted: %v", err)
	}
}
