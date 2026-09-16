package inference

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/audit"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/quota"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

type runnerStore struct {
	op           OperationContext
	advanced     []StepTransition
	retried      []StepTransition
	quotaSaved   []quota.Reservation
	publication  []publication.Publication
	observations []RuntimeObservation
	models       []ModelObservation
	modelStarted int
}

type sequencedRunnerStore struct{ runnerStore }

func (s *sequencedRunnerStore) AdvanceOperationStepCAS(_ context.Context, transition StepTransition) error {
	s.advanced = append(s.advanced, transition)
	s.op.Phase = transition.NextPhase
	s.op.Step = transition.NextStep
	return nil
}

func (s *sequencedRunnerStore) RetryOperationStepCAS(_ context.Context, transition StepTransition, _ time.Duration) error {
	s.retried = append(s.retried, transition)
	s.op.Phase = OperationPending
	return nil
}

func (s *runnerStore) CurrentOperation(context.Context, work.Item) (OperationContext, error) {
	return s.op, nil
}
func (s *runnerStore) AdvanceOperationStepCAS(_ context.Context, t StepTransition) error {
	s.advanced = append(s.advanced, t)
	return nil
}
func (s *runnerStore) RetryOperationStepCAS(_ context.Context, t StepTransition, _ time.Duration) error {
	s.retried = append(s.retried, t)
	return nil
}
func (s *runnerStore) SaveQuotaReservation(_ context.Context, r quota.Reservation) error {
	s.quotaSaved = append(s.quotaSaved, r)
	s.op.Reservation = r
	return nil
}
func (s *runnerStore) SavePublication(_ context.Context, p publication.Publication) error {
	s.publication = append(s.publication, p)
	s.op.Publication = p
	return nil
}
func (s *runnerStore) SaveRuntimeObservation(_ context.Context, _ OperationContext, o RuntimeObservation) error {
	s.observations = append(s.observations, o)
	return nil
}
func (s *runnerStore) SaveModelObservation(_ context.Context, _ OperationContext, o ModelObservation) error {
	s.models = append(s.models, o)
	return nil
}
func (s *runnerStore) MarkModelMaterializing(context.Context, OperationContext) error {
	s.modelStarted++
	return nil
}

type runnerAudit struct{ events []audit.Event }

func (a *runnerAudit) Append(_ context.Context, e audit.Event) error {
	a.events = append(a.events, e)
	return nil
}

type runnerQuota struct {
	reserved, confirmed, released bool
	reserveCalls, confirmCalls    int
	confirmFailures               int
	reservation                   quota.Reservation
}

func (q *runnerQuota) Reserve(context.Context, quota.Reservation) (quota.Reservation, error) {
	q.reserved = true
	q.reserveCalls++
	if q.reservation.TenantID == "" {
		q.reservation = quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "r", State: "reserved"}
	}
	return q.reservation, nil
}
func (q *runnerQuota) Confirm(context.Context, quota.Reservation) error {
	q.confirmed = true
	q.confirmCalls++
	if q.confirmFailures > 0 {
		q.confirmFailures--
		return errors.New("transient quota confirmation failure")
	}
	return nil
}
func (q *runnerQuota) Release(context.Context, quota.Reservation) error {
	q.released = true
	return nil
}
func (q *runnerQuota) Get(context.Context, string, string) (quota.Reservation, error) {
	if q.reservation.ReservationID == "" {
		return quota.Reservation{}, quota.ErrReservationNotFound
	}
	return q.reservation, nil
}

type runnerPublication struct{ withdrawn, withdrawalConfirmed, published, publishConfirmed bool }

func (p *runnerPublication) Withdraw(context.Context, publication.Publication) error {
	p.withdrawn = true
	return nil
}
func (p *runnerPublication) ConfirmWithdrawn(context.Context, publication.Publication) (bool, error) {
	return p.withdrawalConfirmed, nil
}
func (p *runnerPublication) Publish(context.Context, publication.Publication) error {
	p.published = true
	return nil
}
func (p *runnerPublication) ConfirmPublished(context.Context, publication.Publication) (bool, error) {
	return p.publishConfirmed, nil
}

type runnerRuntime struct {
	applyCR, applyRuntime, deleteRuntime, observeRuntime, observeAbsence bool
	observation                                                          RuntimeObservation
	observations                                                         []RuntimeObservation
}

type runnerModel struct{ observation ModelObservation }

func (m *runnerModel) EnsureModel(context.Context, OperationContext) (ModelObservation, error) {
	return m.observation, nil
}

func (r *runnerRuntime) ApplyCR(context.Context, OperationContext) error {
	r.applyCR = true
	return nil
}
func (r *runnerRuntime) ApplyRuntime(context.Context, OperationContext) error {
	r.applyRuntime = true
	return nil
}
func (r *runnerRuntime) ObserveRuntime(context.Context, OperationContext) (RuntimeObservation, error) {
	r.observeRuntime = true
	if len(r.observations) > 0 {
		observation := r.observations[0]
		r.observations = r.observations[1:]
		return observation, nil
	}
	return r.observation, nil
}
func (r *runnerRuntime) DeleteRuntime(context.Context, OperationContext) error {
	r.deleteRuntime = true
	return nil
}
func (r *runnerRuntime) ObserveAbsence(context.Context, OperationContext) (RuntimeObservation, error) {
	r.observeAbsence = true
	return r.observation, nil
}
func (r *runnerRuntime) DeleteCR(context.Context, OperationContext) error { return nil }

func item() work.Item {
	return work.Item{TenantID: "t", ServiceID: "s", Generation: 1, LeaseToken: "lease"}
}
func operation(step string) OperationContext {
	return OperationContext{TenantID: "t", ServiceID: "s", ID: "o", Kind: "create", Phase: OperationRunning, Step: step, TargetGeneration: 1, Publication: publication.Publication{TenantID: "t", ServiceID: "s", Generation: 1, State: "withdrawn"}}
}

func TestRunnerMissingProviderRetriesOperation(t *testing.T) {
	store := &runnerStore{op: operation(string(StepApplyRuntime))}
	runner := &Runner{Store: store, Audit: &runnerAudit{}}
	_, err := runner.Execute(context.Background(), item())
	if err == nil || !errors.Is(err, ErrOperationProviderMissing) {
		t.Fatalf("Execute error=%v, want missing provider", err)
	}
	if len(store.retried) != 1 || store.retried[0].ExpectedStep != string(StepApplyRuntime) {
		t.Fatalf("retry=%+v, want one apply_runtime retry", store.retried)
	}
	if len(store.advanced) != 0 {
		t.Fatalf("missing provider advanced operation: %+v", store.advanced)
	}
}

func TestRunnerCarriesDurableAuditContextAndState(t *testing.T) {
	store := &runnerStore{op: operation(string(StepApplyRuntime))}
	store.op.Actor = "workload:caller"
	store.op.RequestID = "request-123"
	runtime := &runnerRuntime{}
	auditSink := &runnerAudit{}
	runner := &Runner{Store: store, Runtime: runtime, Audit: auditSink}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("Execute error=%v", err)
	}
	if len(auditSink.events) != 1 {
		t.Fatalf("audit events=%d, want one", len(auditSink.events))
	}
	event := auditSink.events[0]
	if event.Actor != "workload:caller" || event.RequestID != "request-123" {
		t.Fatalf("audit context actor=%q request=%q", event.Actor, event.RequestID)
	}
	if string(event.BeforeState) == "" || string(event.AfterState) == "" {
		t.Fatalf("audit state before=%q after=%q, want both populated", event.BeforeState, event.AfterState)
	}
}

func TestRunnerRetryCarriesDurableAuditContextAndState(t *testing.T) {
	store := &runnerStore{op: operation(string(StepApplyRuntime))}
	store.op.Actor = "workload:caller"
	store.op.RequestID = "request-123"
	auditSink := &runnerAudit{}
	_, err := (&Runner{Store: store, Audit: auditSink}).Execute(context.Background(), item())
	if err == nil {
		t.Fatal("Execute error=nil, want provider retry")
	}
	if len(auditSink.events) != 1 {
		t.Fatalf("audit events=%d, want one", len(auditSink.events))
	}
	event := auditSink.events[0]
	if event.EventType != "inference.operation.step.retry" || event.Actor != "workload:caller" || event.RequestID != "request-123" {
		t.Fatalf("retry audit event=%+v", event)
	}
	if string(event.BeforeState) == "" || string(event.AfterState) == "" {
		t.Fatalf("retry audit state before=%q after=%q, want both populated", event.BeforeState, event.AfterState)
	}
}

func TestRunnerRequiresRuntimeAndModelBeforePublish(t *testing.T) {
	store := &runnerStore{op: operation(string(StepObserveRuntime))}
	runtime := &runnerRuntime{observation: RuntimeObservation{Ready: true, ModelReady: true, ModelReadyKnown: true, InvocationHealthy: false, InvocationKnown: true, Reason: "probe pending"}}
	runner := &Runner{Store: store, Runtime: runtime, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("Execute error=%v, want runtime/model ready despite unpublished invocation", err)
	}
	if len(store.retried) != 0 || len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepPublish) {
		t.Fatalf("state writes advanced=%+v retried=%+v", store.advanced, store.retried)
	}
	if store.advanced[0].ExpectedStep != string(StepObserveRuntime) {
		t.Fatalf("advance=%+v, want observe_runtime", store.advanced)
	}
	store.op.Step = string(StepVerifyInvocation)
	store.op.Publication = publication.Publication{TenantID: "t", ServiceID: "s", Generation: 1, State: "published"}
	if _, err := runner.Execute(context.Background(), item()); err == nil || !errors.Is(err, ErrOperationNotReady) {
		t.Fatalf("verify error=%v, want invocation retry", err)
	}
	runtime.observation.InvocationHealthy = true
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("healthy verify error=%v", err)
	}
	if len(store.advanced) != 2 || store.advanced[1].NextStep != string(StepComplete) {
		t.Fatalf("verify advance=%+v, want complete", store.advanced)
	}
}

func TestRunnerVerifyInvocationRejectsUnconfirmedOrStalePublication(t *testing.T) {
	for _, tc := range []struct {
		name        string
		publication publication.Publication
	}{
		{name: "missing", publication: publication.Publication{}},
		{name: "old generation", publication: publication.Publication{TenantID: "t", ServiceID: "s", Generation: 0, State: "published"}},
		{name: "not observed", publication: publication.Publication{TenantID: "t", ServiceID: "s", Generation: 1, State: "publishing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &runnerStore{op: operation(string(StepVerifyInvocation))}
			store.op.Publication = tc.publication
			runtime := &runnerRuntime{observation: RuntimeObservation{Ready: true, ModelReady: true, ModelReadyKnown: true, InvocationKnown: true, InvocationHealthy: true}}
			runner := &Runner{Store: store, Runtime: runtime, Audit: &runnerAudit{}}
			if _, err := runner.Execute(context.Background(), item()); err == nil || !errors.Is(err, ErrOperationNotReady) {
				t.Fatalf("Execute error=%v, want retryable publication fence error", err)
			}
			if runtime.observeRuntime || len(store.advanced) != 0 || len(store.retried) != 1 {
				t.Fatalf("stale publication reached probe or advanced: observed=%v advanced=%+v retried=%+v", runtime.observeRuntime, store.advanced, store.retried)
			}
		})
	}
}

func TestRunnerWithdrawsAndConfirmsPublicationBeforeRuntimeDelete(t *testing.T) {
	store := &runnerStore{op: OperationContext{TenantID: "t", ServiceID: "s", ID: "o", Kind: "stop", Phase: OperationRunning, Step: string(StepWithdrawPublication), TargetGeneration: 1}}
	publicationProvider := &runnerPublication{}
	runtime := &runnerRuntime{}
	runner := &Runner{Store: store, Publication: publicationProvider, Runtime: runtime, Audit: &runnerAudit{}}
	_, err := runner.Execute(context.Background(), item())
	if err == nil || !errors.Is(err, ErrOperationNotReady) {
		t.Fatalf("unconfirmed withdrawal error=%v, want not ready", err)
	}
	if runtime.deleteRuntime || len(store.advanced) != 0 {
		t.Fatalf("runtime/delete advanced before withdrawal confirmation")
	}
	if len(store.publication) != 1 || store.publication[0].State != "withdrawing" {
		t.Fatalf("withdraw intent=%+v, want persisted withdrawing state before provider call", store.publication)
	}
	publicationProvider.withdrawalConfirmed = true
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("confirmed withdrawal error=%v", err)
	}
	if !publicationProvider.withdrawn || len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepDeleteRuntime) {
		t.Fatalf("withdraw=%v advance=%+v", publicationProvider.withdrawn, store.advanced)
	}
}

func TestRunnerQuotaReserveConfirmPersistsBeforeApply(t *testing.T) {
	store := &runnerStore{op: operation(string(StepReserveQuota))}
	store.op.Reservation = quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "r", State: "pending"}
	q := &runnerQuota{}
	runner := &Runner{Store: store, Quota: q, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("reserve Execute error=%v", err)
	}
	if !q.reserved || !q.confirmed || len(store.quotaSaved) != 1 || store.quotaSaved[0].State != "confirmed" {
		t.Fatalf("quota calls reserved=%v confirmed=%v saved=%+v", q.reserved, q.confirmed, store.quotaSaved)
	}
	if len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepApplyCR) {
		t.Fatalf("advance=%+v", store.advanced)
	}
}

func TestRunnerQuotaRetryConfirmsPersistedReservationWithoutReReserve(t *testing.T) {
	store := &runnerStore{op: operation(string(StepReserveQuota))}
	store.op.Reservation = quota.Reservation{
		TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1",
		ReservationID: "pending-o", State: "pending",
	}
	q := &runnerQuota{confirmFailures: 1}
	runner := &Runner{Store: store, Quota: q, Audit: &runnerAudit{}}

	if _, err := runner.Execute(context.Background(), item()); err == nil {
		t.Fatal("first confirmation should fail and be retried")
	}
	if q.reserveCalls != 1 || q.confirmCalls != 1 {
		t.Fatalf("first attempt reserve=%d confirm=%d, want 1/1", q.reserveCalls, q.confirmCalls)
	}
	if len(store.quotaSaved) != 1 || store.quotaSaved[0].State != "reserved" || store.quotaSaved[0].LastErrorCode != "quota_confirm_failed" {
		t.Fatalf("persisted reservation=%+v, want reserved", store.quotaSaved)
	}

	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("retry confirmation error=%v", err)
	}
	if q.reserveCalls != 1 || q.confirmCalls != 2 {
		t.Fatalf("retry reserve=%d confirm=%d, want reserve unchanged and confirm=2", q.reserveCalls, q.confirmCalls)
	}
	if len(store.quotaSaved) != 2 || store.quotaSaved[1].State != "confirmed" || store.quotaSaved[1].LastErrorCode != "" {
		t.Fatalf("retry persistence=%+v, want confirmed", store.quotaSaved)
	}
	if len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepApplyCR) {
		t.Fatalf("retry transition=%+v, want apply_cr", store.advanced)
	}
}

func TestRunnerQuotaRecoversRemoteReservationBeforeReserve(t *testing.T) {
	store := &runnerStore{op: operation(string(StepReserveQuota))}
	store.op.Reservation = quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "pending-o", State: "pending"}
	q := &runnerQuota{reservation: quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "pending-o", State: "reserved"}}
	runner := &Runner{Store: store, Quota: q, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("remote reservation recovery error=%v", err)
	}
	if q.reserveCalls != 0 || q.confirmCalls != 1 {
		t.Fatalf("recovery reserve=%d confirm=%d, want 0/1", q.reserveCalls, q.confirmCalls)
	}
	if len(store.quotaSaved) != 1 || store.quotaSaved[0].State != "confirmed" {
		t.Fatalf("recovered persistence=%+v, want confirmed", store.quotaSaved)
	}
}

func TestRunnerQuotaPersistsRemoteConfirmedBeforeApplyCR(t *testing.T) {
	store := &runnerStore{op: operation(string(StepReserveQuota))}
	store.op.Reservation = quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "pending-o", State: "pending"}
	q := &runnerQuota{reservation: quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "pending-o", State: "confirmed"}}
	runner := &Runner{Store: store, Quota: q, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("remote confirmed recovery error=%v", err)
	}
	if q.reserveCalls != 0 || q.confirmCalls != 0 {
		t.Fatalf("recovery reserve=%d confirm=%d, want 0/0", q.reserveCalls, q.confirmCalls)
	}
	if len(store.quotaSaved) != 1 || store.quotaSaved[0].State != "confirmed" || store.quotaSaved[0].LeaseToken != "lease" {
		t.Fatalf("recovered confirmed persistence=%+v", store.quotaSaved)
	}
}

func TestRunnerCreateLifecycleAdvancesDurableStepsBeforePublish(t *testing.T) {
	store := &sequencedRunnerStore{runnerStore: runnerStore{op: OperationContext{
		TenantID: "t", ServiceID: "s", ID: "o", Kind: "create", Phase: OperationPending,
		Step: string(StepAdmission), TargetGeneration: 1,
		Publication: publication.Publication{TenantID: "t", ServiceID: "s", Generation: 1, State: "withdrawn"},
		Reservation: quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "o", Generation: "1", ReservationID: "pending-o", State: "pending"},
	}}}
	quotaProvider := &runnerQuota{}
	publicationProvider := &runnerPublication{publishConfirmed: true}
	runtimeProvider := &runnerRuntime{observations: []RuntimeObservation{
		{Ready: true, RuntimePhase: "ready", ModelReady: true, ModelReadyKnown: true,
			InvocationHealthy: false, InvocationKnown: false, Reason: "endpoint is unpublished"},
		{Ready: true, RuntimePhase: "ready", ModelReady: true, ModelReadyKnown: true,
			InvocationHealthy: true, InvocationKnown: true, Reason: "published endpoint is healthy"},
	}}
	runner := &Runner{
		Store: store, Admission: admissionFunc(func(context.Context, OperationContext) error { return nil }),
		Model: &runnerModel{observation: ModelObservation{Known: true, Ready: true}},
		Quota: quotaProvider, Publication: publicationProvider, Runtime: runtimeProvider,
		Audit: &runnerAudit{},
	}
	for i := 0; i < 8 && store.op.Phase != OperationSucceeded; i++ {
		if _, err := runner.Execute(context.Background(), item()); err != nil {
			t.Fatalf("create step %d: %v", i, err)
		}
	}
	if store.op.Phase != OperationSucceeded || store.op.Step != string(StepComplete) {
		t.Fatalf("final operation = %s/%s, want succeeded/complete", store.op.Phase, store.op.Step)
	}
	if len(store.retried) != 0 || !quotaProvider.reserved || !quotaProvider.confirmed || !runtimeProvider.applyCR || !runtimeProvider.applyRuntime || !runtimeProvider.observeRuntime || store.modelStarted != 1 {
		t.Fatalf("lifecycle side effects retried=%+v quota=%+v runtime=%+v model_started=%d", store.retried, quotaProvider, runtimeProvider, store.modelStarted)
	}
	if len(runtimeProvider.observations) != 0 {
		t.Fatalf("invocation verification did not consume a post-publication observation: remaining=%+v", runtimeProvider.observations)
	}
	if !publicationProvider.published || len(store.publication) < 2 || store.publication[0].State != "publishing" || store.publication[len(store.publication)-1].State != "published" {
		t.Fatalf("publication writes=%+v published=%v", store.publication, publicationProvider.published)
	}
	wantSteps := []string{string(StepAdmission), string(StepReserveQuota), string(StepApplyCR), string(StepMaterializeModel), string(StepApplyRuntime), string(StepObserveRuntime), string(StepPublish), string(StepVerifyInvocation)}
	if len(store.advanced) != len(wantSteps)+1 {
		t.Fatalf("durable transitions=%d, want %d: %+v", len(store.advanced), len(wantSteps)+1, store.advanced)
	}
	for i, transition := range store.advanced[1:] {
		if transition.ExpectedStep != wantSteps[i] {
			t.Fatalf("transition %d expected step=%q, want %q", i, transition.ExpectedStep, wantSteps[i])
		}
	}
}

type admissionFunc func(context.Context, OperationContext) error

func (f admissionFunc) Admit(ctx context.Context, op OperationContext) error { return f(ctx, op) }

func TestRunnerMaterializesModelBeforeRuntimeApply(t *testing.T) {
	store := &runnerStore{op: operation(string(StepMaterializeModel))}
	model := &runnerModel{observation: ModelObservation{Known: true, Ready: true, Reason: "artifact mounted"}}
	runner := &Runner{Store: store, Model: model, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("materialize Execute error=%v", err)
	}
	if store.modelStarted != 1 || len(store.models) != 1 || !store.models[0].Ready || len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepApplyRuntime) {
		t.Fatalf("started=%d models=%+v advance=%+v, want persisted materialization then apply_runtime", store.modelStarted, store.models, store.advanced)
	}
}

func TestRunnerRetriesWhenModelMaterializationIsUnknown(t *testing.T) {
	store := &runnerStore{op: operation(string(StepMaterializeModel))}
	runner := &Runner{Store: store, Model: &runnerModel{observation: ModelObservation{Known: false, Reason: "artifact still downloading"}}, Audit: &runnerAudit{}}
	_, err := runner.Execute(context.Background(), item())
	if err == nil || !errors.Is(err, ErrOperationNotReady) {
		t.Fatalf("error=%v, want retryable model readiness error", err)
	}
	if len(store.advanced) != 0 || len(store.retried) != 1 || store.modelStarted != 1 || len(store.models) != 0 {
		t.Fatalf("advanced=%+v retried=%+v started=%d models=%+v", store.advanced, store.retried, store.modelStarted, store.models)
	}
}

func TestRunnerPersistsPublishingIntentBeforeProviderCall(t *testing.T) {
	store := &runnerStore{op: operation(string(StepPublish))}
	provider := &runnerPublication{publishConfirmed: true}
	runner := &Runner{Store: store, Publication: provider, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), item()); err != nil {
		t.Fatalf("publish Execute error=%v", err)
	}
	if len(store.publication) < 2 || store.publication[0].State != "publishing" || store.publication[len(store.publication)-1].State != "published" {
		t.Fatalf("publication writes=%+v, want publishing then published", store.publication)
	}
}

func TestRunnerReleasesPreviousQuotaBeforeUpdateReserve(t *testing.T) {
	store := &runnerStore{op: OperationContext{
		TenantID: "t", ServiceID: "s", ID: "o", Kind: "update", Phase: OperationRunning,
		Step: string(StepObserveAbsence), TargetGeneration: 2,
		PreviousReservation: quota.Reservation{TenantID: "t", ServiceID: "s", OperationID: "old", Generation: "1", ReservationID: "old-r", State: "confirmed"},
	}}
	runtime := &runnerRuntime{observation: RuntimeObservation{Absent: true, RuntimePhase: "stopped"}}
	runner := &Runner{Store: store, Runtime: runtime, Audit: &runnerAudit{}}
	if _, err := runner.Execute(context.Background(), work.Item{TenantID: "t", ServiceID: "s", Generation: 2, LeaseToken: "lease"}); err != nil {
		t.Fatalf("observe absence error=%v", err)
	}
	if len(store.advanced) != 1 || store.advanced[0].NextStep != string(StepReleasePreviousQuota) {
		t.Fatalf("advance=%+v, want release_previous_quota before reserve", store.advanced)
	}
}

func TestRunnerRejectsInvalidOperationIdentity(t *testing.T) {
	store := &runnerStore{op: operation(string(StepApplyRuntime))}
	store.op.TenantID = "other"
	runner := &Runner{Store: store, Audit: &runnerAudit{}}
	_, err := runner.Execute(context.Background(), item())
	if !errors.Is(err, work.ErrStaleGeneration) {
		t.Fatalf("error=%v, want stale generation", err)
	}
	if strings.Contains(err.Error(), "provider") {
		t.Fatal("identity error reached provider")
	}
}
