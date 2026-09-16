package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

type fakeRepository struct {
	current   Desired
	committed bool
	result    Observation
}

type durableFakeRepository struct {
	desired Desired
	saved   bool
}

func (f *durableFakeRepository) CurrentForWork(context.Context, work.Item) (Desired, error) {
	return f.desired, nil
}
func (f *durableFakeRepository) Current(context.Context, string, string) (Desired, error) {
	return f.desired, nil
}
func (f *durableFakeRepository) SaveObservation(context.Context, string, string, int64, Observation) error {
	f.saved = true
	return nil
}
func (f *durableFakeRepository) SaveObservationForWork(_ context.Context, _ work.Item, o Observation) error {
	if o.Generation != f.desired.Generation {
		return ErrStaleGeneration
	}
	f.saved = true
	return nil
}

func TestReconcilerExecuteRequiresLeaseScopedRepository(t *testing.T) {
	r := Reconciler{Repository: &fakeRepository{current: Desired{TenantID: "t", ServiceID: "s", Generation: 1}}, Runtime: RuntimeFunc(func(context.Context, Desired) (Observation, error) { return Observation{Generation: 1}, nil })}
	_, err := r.Execute(context.Background(), work.Item{TenantID: "t", ServiceID: "s", Generation: 1, LeaseToken: "lease"})
	if err == nil {
		t.Fatal("Execute accepted a repository without lease-scoped methods")
	}
	if !errors.Is(err, ErrLeaseRepositoryRequired) {
		t.Fatalf("Execute() error = %v, want %v", err, ErrLeaseRepositoryRequired)
	}
}

func TestReconcilerExecuteCommitsOnlyMatchingLeasedGeneration(t *testing.T) {
	repo := &durableFakeRepository{desired: Desired{TenantID: "t", ServiceID: "s", Generation: 2}}
	r := Reconciler{Repository: repo, Runtime: RuntimeFunc(func(_ context.Context, d Desired) (Observation, error) {
		return Observation{Generation: d.Generation, RuntimePhase: "ready"}, nil
	})}
	result, err := r.Execute(context.Background(), work.Item{TenantID: "t", ServiceID: "s", Generation: 2, LeaseToken: "lease"})
	if err != nil || result.Generation != 2 || !repo.saved {
		t.Fatalf("Execute() result=%+v err=%v saved=%v", result, err, repo.saved)
	}
	_, err = r.Execute(context.Background(), work.Item{TenantID: "t", ServiceID: "s", Generation: 1, LeaseToken: "lease"})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale Execute() error=%v", err)
	}
}

func (f *fakeRepository) Current(context.Context, string, string) (Desired, error) {
	return f.current, nil
}
func (f *fakeRepository) SaveObservation(_ context.Context, tenant, service string, generation int64, observation Observation) error {
	if tenant != f.current.TenantID || service != f.current.ServiceID || generation != f.current.Generation {
		return ErrStaleGeneration
	}
	f.result, f.committed = observation, true
	return nil
}

func TestReconcilerReadsCurrentGenerationBeforeCommit(t *testing.T) {
	repo := &fakeRepository{current: Desired{TenantID: "t1", ServiceID: "s1", Generation: 2}}
	r := Reconciler{
		Repository: repo,
		Runtime: RuntimeFunc(func(context.Context, Desired) (Observation, error) {
			return Observation{Generation: 1}, nil
		}),
	}
	if err := r.Reconcile(context.Background(), "t1", "s1"); err != ErrStaleGeneration {
		t.Fatalf("Reconcile() error = %v, want stale generation", err)
	}
	if repo.committed {
		t.Fatal("stale observation was committed")
	}
}
