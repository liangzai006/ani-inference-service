package work

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type loopStore struct {
	calls   []string
	onClaim func(string) error
}

func (s *loopStore) Claim(_ context.Context, tenant string) (Item, bool, error) {
	s.calls = append(s.calls, tenant)
	if s.onClaim != nil {
		return Item{}, false, s.onClaim(tenant)
	}
	return Item{}, false, nil
}
func (s *loopStore) Commit(context.Context, Item, Result) error { return nil }
func (s *loopStore) Retry(context.Context, Item, error) error   { return nil }

type loopTenants []string

func (tenants loopTenants) ListTenants(context.Context) ([]string, error) {
	return tenants, nil
}

func TestLoopRunOnceClaimsEveryTenant(t *testing.T) {
	store := &loopStore{}
	loop := Loop{Worker: &Worker{Store: store, Execute: func(context.Context, Item) (Result, error) {
		return Result{}, nil
	}}, Tenants: loopTenants{"tenant-a", "tenant-b"}}
	if err := loop.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 2 || store.calls[0] != "tenant-a" || store.calls[1] != "tenant-b" {
		t.Fatalf("claims = %#v, want both tenants", store.calls)
	}
}

func TestLoopRejectsInvalidConfiguration(t *testing.T) {
	for name, loop := range map[string]*Loop{
		"nil loop":     nil,
		"nil worker":   {},
		"nil store":    {Worker: &Worker{}},
		"nil executor": {Worker: &Worker{Store: &loopStore{}}, Tenants: loopTenants{"tenant-a"}},
		"nil tenants":  {Worker: &Worker{Store: &loopStore{}, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := loop.RunOnce(ctx); err == nil {
				t.Fatal("RunOnce accepted invalid configuration")
			}
			if err := loop.Run(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run must reject configuration immediately, got %v", err)
			}
		})
	}
}

func TestLoopCancellationStopsBeforeNextTenant(t *testing.T) {
	for _, cancelBefore := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		store := &loopStore{onClaim: func(string) error { cancel(); return nil }}
		loop := Loop{Worker: &Worker{Store: store, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }}, Tenants: loopTenants{"tenant-a", "tenant-b"}}
		if cancelBefore {
			cancel()
		}
		err := loop.RunOnce(ctx)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelBefore=%t: error = %v", cancelBefore, err)
		}
		want := 1
		if cancelBefore {
			want = 0
		}
		if len(store.calls) != want {
			t.Fatalf("cancelBefore=%t: claims = %v", cancelBefore, store.calls)
		}
	}
}

func TestLoopContinuesAfterTenantFailureAndReportsScope(t *testing.T) {
	failure := errors.New("database unavailable")
	store := &loopStore{onClaim: func(tenant string) error {
		if tenant == "tenant-a" {
			return failure
		}
		return nil
	}}
	loop := Loop{Worker: &Worker{Store: store, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }}, Tenants: loopTenants{"tenant-a", "tenant-b"}}
	err := loop.RunOnce(context.Background())
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "tenant-a") || len(store.calls) != 2 {
		t.Fatalf("error = %v, claims = %v", err, store.calls)
	}
}

func TestLoopRetriesFailuresAndStopsWithoutReportingCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	failure := errors.New("temporary claim failure")
	store := &loopStore{}
	store.onClaim = func(string) error {
		if len(store.calls) == 1 {
			return failure
		}
		cancel()
		return context.Canceled
	}
	var reports []error
	loop := Loop{
		Worker:  &Worker{Store: store, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }},
		Tenants: loopTenants{"tenant-a"}, Interval: time.Millisecond,
		OnError: func(err error) { reports = append(reports, err) },
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(store.calls) != 2 || len(reports) != 1 || !errors.Is(reports[0], failure) {
		t.Fatalf("claims = %v, reports = %v", store.calls, reports)
	}
}

func TestLoopRunsDurableRecoveryBeforeFirstScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	store := &loopStore{onClaim: func(string) error {
		calls = append(calls, "claim")
		cancel()
		return nil
	}}
	loop := Loop{
		Worker:  &Worker{Store: store, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }},
		Tenants: loopTenants{"tenant-a"}, Interval: time.Millisecond,
		Recover: func(context.Context) error {
			calls = append(calls, "recover")
			return nil
		},
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if len(calls) < 2 || calls[0] != "recover" || calls[1] != "claim" {
		t.Fatalf("call order = %v, want recovery before first claim", calls)
	}
}

func TestLoopRetriesDurableRecoveryPeriodically(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recoveries := 0
	loop := Loop{
		Worker:  &Worker{Store: &loopStore{}, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }},
		Tenants: loopTenants{"tenant-a"}, Interval: time.Millisecond, RecoveryInterval: 2 * time.Millisecond,
		Recover: func(context.Context) error {
			recoveries++
			if recoveries >= 2 {
				cancel()
			}
			return nil
		},
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if recoveries < 2 {
		t.Fatalf("recovery calls = %d, want periodic retry", recoveries)
	}
}

func TestLoopUsesRepairAfterStartupRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startup, repairs := 0, 0
	loop := Loop{
		Worker:  &Worker{Store: &loopStore{}, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }},
		Tenants: loopTenants{"tenant-a"}, Interval: time.Millisecond, RecoveryInterval: 2 * time.Millisecond,
		Recover: func(context.Context) error { startup++; return nil },
		Repair: func(context.Context) error {
			repairs++
			if repairs >= 2 {
				cancel()
			}
			return nil
		},
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if startup != 1 || repairs < 2 {
		t.Fatalf("startup recoveries=%d repairs=%d, want one startup and periodic repairs", startup, repairs)
	}
}

func TestLoopContinuesWhenDurableRecoveryFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("postgres unavailable")
	var reports []error
	claims := 0
	loop := Loop{
		Worker: &Worker{Store: &loopStore{onClaim: func(string) error {
			claims++
			if claims == 1 {
				cancel()
			}
			return nil
		}}, Execute: func(context.Context, Item) (Result, error) { return Result{}, nil }},
		Tenants: loopTenants{"tenant-a"}, Interval: time.Millisecond,
		Recover: func(context.Context) error { return failure },
		OnError: func(err error) { reports = append(reports, err) },
	}
	if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want cancellation", err)
	}
	if claims != 1 || len(reports) != 1 || !errors.Is(reports[0], failure) {
		t.Fatalf("claims=%d reports=%v, want one recovery report and continued scan", claims, reports)
	}
}
