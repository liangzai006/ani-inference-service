package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/audit"
	inference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/quota"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

// The same persisted service is replaced, stopped, started and finally deleted.
// Each step reconstructs the worker; only the external providers are fixtures.
func TestPostgresReplacementAndDeletionLifecycle(t *testing.T) {
	for _, interruptStep := range []string{"none", "release_quota"} {
		t.Run(interruptStep, func(t *testing.T) {
			testReplacementLifecycle(t, interruptStep)
		})
	}
}

func testReplacementLifecycle(t *testing.T, interruptStep string) {
	t.Helper()
	ctx, pool, tenant, service := lifecycleDatabase(t)
	resourceJSON := []byte(`{"requests":{"cpu":"2"},"limits":{"cpu":"4"}}`)
	created, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "replacement-" + service.String(),
		ModelVersionID: uuid.NewString(), Resources: resourceJSON, RequestHash: "create", IdempotencyKey: "create", Replicas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &replacementProvider{lifecycleProvider: lifecycleProvider{t: t, pool: pool, resources: resourceJSON}, generation: 1}
	interruption := &lifecycleInterruption{step: interruptStep}
	runLifecycleOperation(t, ctx, pool, tenant, created.OperationID, p, interruption)
	for _, kind := range []string{"update", "restart", "stop", "start", "stop", "delete"} {
		p.generation++
		var operationID string
		if kind == "update" {
			p.resources = []byte(`{"requests":{"cpu":"3"},"limits":{"cpu":"6"}}`)
			response, err := NewUpdateUseCase(NewRepository(pool)).Update(ctx, inference.UpdateInput{
				TenantID: tenant.String(), ServiceID: service.String(), RequestID: kind, RequestHash: kind,
				ExpectedGeneration: p.generation - 1, Replicas: 1, RuntimeMode: "leader_worker_set", WorkerReplicas: 3,
				Resources: resources.Normalized{Requests: map[string]string{"cpu": "3"}, Limits: map[string]string{"cpu": "6"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			operationID = response.GetOperation().GetId()
		} else {
			response, err := NewCommandUseCase(NewRepository(pool)).Command(ctx, inference.CommandInput{
				TenantID: tenant.String(), ServiceID: service.String(), Kind: kind,
				RequestID: fmt.Sprintf("%s-%d", kind, p.generation), RequestHash: kind, ExpectedGeneration: p.generation - 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			operationID = response.GetOperation().GetId()
		}
		runLifecycleOperation(t, ctx, pool, tenant, operationID, p, interruption)
		if kind == "update" || kind == "restart" || kind == "start" {
			pub, err := New(pool).GetPublication(ctx, GetPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: p.generation})
			if err != nil || pub.ObservedPhase != "published" {
				t.Fatalf("%s generation %d did not publish: %+v err=%v", kind, p.generation, pub, err)
			}
		}
	}
	stored, err := New(pool).GetServiceForUpdate(ctx, GetServiceForUpdateParams{TenantID: pgUUID(tenant), ID: pgUUID(service)})
	if err != nil || !stored.DeletedAt.Valid {
		t.Fatalf("service not tombstoned: %+v err=%v", stored, err)
	}
	w, err := New(pool).GetResourceWork(ctx, GetResourceWorkParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service)})
	if err != nil || w.LeaseToken.Valid || w.NextRunAt.Valid || w.AcknowledgedVersion != w.DirtyVersion {
		t.Fatalf("delete left work unacknowledged or scheduled: %+v err=%v", w, err)
	}
	if len(p.released) != 4 { // create, update, restart and start allocations
		t.Fatalf("release count=%d, want 4: %v", len(p.released), p.released)
	}
	if interruption.triggered != (interruptStep != "none") {
		t.Fatalf("interruption was not exercised: %+v", interruption)
	}
	// Historical publications stay withdrawn. The new generation must not
	// overwrite their rows even when operation objects are rebuilt each step.
	for _, generation := range []int64{1, 2, 3, 5} {
		pub, err := New(pool).GetPublication(ctx, GetPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: generation})
		if err != nil || pub.ObservedPhase != "withdrawn" {
			t.Fatalf("old publication generation %d: %+v err=%v", generation, pub, err)
		}
	}
}

func runLifecycleOperation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID, operationID string, p *replacementProvider, interruption *lifecycleInterruption) {
	t.Helper()
	for {
		store := &interruptedLifecycleStore{OperationStore: NewOperationStore(pool), interruption: interruption}
		runner := &inference.Runner{Store: store, Admission: p, Model: p, Quota: p, Publication: p, Runtime: p, RetryAfter: time.Millisecond}
		worker := work.Worker{Store: NewWorkStore(pool, WorkStoreOptions{LeaseSeconds: 2, ObserveSeconds: .001, RetrySeconds: .001}), Execute: runner.Execute}
		if err := worker.RunOnce(ctx, tenant.String()); err != nil && err != errLifecycleTransitionInterrupted {
			t.Fatalf("execute generation %d: %v", p.generation, err)
		}
		op, err := New(pool).GetOperation(ctx, GetOperationParams{TenantID: pgUUID(tenant), ID: pgUUID(uuid.MustParse(operationID))})
		if err != nil {
			t.Fatal(err)
		}
		if op.Phase == "succeeded" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s did not finish: %s/%s: %v", op.Kind, op.Phase, op.Step, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

var errLifecycleTransitionInterrupted = errors.New("injected interruption after provider result persistence")

type lifecycleInterruption struct {
	step      string
	triggered bool
}

type interruptedLifecycleStore struct {
	*OperationStore
	interruption *lifecycleInterruption
}

func (s *interruptedLifecycleStore) AdvanceOperationStepCASWithAudit(ctx context.Context, in inference.StepTransition, event audit.Event) error {
	if !s.interruption.triggered && in.TargetGeneration > 1 && in.ExpectedStep == s.interruption.step && in.NextPhase == inference.OperationSucceeded {
		s.interruption.triggered = true
		return errLifecycleTransitionInterrupted
	}
	return s.OperationStore.AdvanceOperationStepCASWithAudit(ctx, in, event)
}

type replacementProvider struct {
	lifecycleProvider
	generation        int64
	runtimeGeneration int64
	published         int64
	lastWithdrawn     int64
	absent            bool
	released          []string
}

func (p *replacementProvider) ApplyRuntime(_ context.Context, op inference.OperationContext) error {
	if p.runtimeGeneration != 0 {
		p.t.Fatal("replacement applied before runtime absence")
	}
	p.runtimeGeneration, p.absent = op.TargetGeneration, false
	return nil
}

func (p *replacementProvider) Reserve(ctx context.Context, in quota.Reservation) (quota.Reservation, error) {
	wantUnits := int64(1)
	if p.generation > 1 {
		wantUnits = 4 // one group with one leader and three workers
	}
	if in.Demand.Units != wantUnits {
		p.t.Fatalf("quota demand units=%d for generation %d, want %d", in.Demand.Units, p.generation, wantUnits)
	}
	return p.lifecycleProvider.Reserve(ctx, in)
}

func (p *replacementProvider) Publish(ctx context.Context, pub publication.Publication) error {
	if pub.Generation != p.generation || pub.Generation != p.runtimeGeneration {
		p.t.Fatalf("published generation %d for desired/runtime %d/%d", pub.Generation, p.generation, p.runtimeGeneration)
	}
	p.published = pub.Generation
	return p.lifecycleProvider.Publish(ctx, pub)
}

func (p *replacementProvider) Withdraw(ctx context.Context, pub publication.Publication) error {
	if p.published != 0 && pub.Generation != p.published {
		p.t.Fatalf("withdrawing generation %d while generation %d is published", pub.Generation, p.published)
	}
	row, err := New(p.pool).GetPublication(ctx, GetPublicationParams{TenantID: pgUUID(uuid.MustParse(pub.TenantID)), ServiceID: pgUUID(uuid.MustParse(pub.ServiceID)), Generation: pub.Generation})
	if err != nil || row.DesiredPhase != "withdrawing" {
		p.t.Fatalf("withdraw intent was not persisted: %+v err=%v", row, err)
	}
	p.lastWithdrawn = pub.Generation
	return nil
}

func (p *replacementProvider) ConfirmWithdrawn(context.Context, publication.Publication) (bool, error) {
	p.published = 0
	return true, nil
}

func (p *replacementProvider) DeleteRuntime(ctx context.Context, op inference.OperationContext) error {
	row, err := New(p.pool).GetPublication(ctx, GetPublicationParams{TenantID: pgUUID(uuid.MustParse(op.TenantID)), ServiceID: pgUUID(uuid.MustParse(op.ServiceID)), Generation: p.lastWithdrawn})
	if err != nil || row.ObservedPhase != "withdrawn" || p.published != 0 {
		p.t.Fatalf("runtime deleted before confirmed withdrawal: %+v err=%v", row, err)
	}
	p.runtimeGeneration = 0
	return nil
}

func (p *replacementProvider) ObserveAbsence(context.Context, inference.OperationContext) (inference.RuntimeObservation, error) {
	p.absent = p.runtimeGeneration == 0
	return inference.RuntimeObservation{Absent: p.absent}, nil
}

func (p *replacementProvider) DeleteCR(context.Context, inference.OperationContext) error {
	if !p.absent || p.published != 0 {
		p.t.Fatal("CR deletion before runtime absence or withdrawal")
	}
	return nil
}

func (p *replacementProvider) Release(_ context.Context, r quota.Reservation) error {
	if !p.absent || p.published != 0 || r.OperationID == "" || r.ReservationID == "" {
		p.t.Fatalf("invalid release before absence or without reservation identity: %+v", r)
	}
	for _, id := range p.released {
		if id == r.ReservationID {
			p.t.Fatalf("already persisted released reservation sent again: %s", id)
		}
	}
	p.released = append(p.released, r.ReservationID)
	return nil
}
