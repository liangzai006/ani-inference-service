package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

func TestLoopServerStartBlocksAndReturnsLoopResult(t *testing.T) {
	tenants := &drainingLoopTenants{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	s, err := NewLoopServer(loopServerStore{}, tenants, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{}, nil
	}, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- s.Start(context.Background()) }()
	<-tenants.started
	select {
	case err := <-startResult:
		close(tenants.release)
		_ = s.Stop(context.Background())
		t.Fatalf("Start returned before the loop stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- s.Stop(context.Background()) }()
	<-tenants.canceled
	close(tenants.release)
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want loop cancellation", err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestLoopServerWaitsForDependencyBeforeDurableScan(t *testing.T) {
	ready := make(chan struct{})
	claimed := make(chan struct{}, 1)
	store := &loopServerStore{claim: func() { claimed <- struct{}{} }}
	loop := &work.Loop{Worker: &work.Worker{Store: store, Execute: func(context.Context, work.Item) (work.Result, error) { return work.Result{}, nil }}, Tenants: loopServerTenants{}, Interval: time.Hour}
	s := &LoopServer{Loop: loop, WaitReady: func(ctx context.Context) error {
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()
	select {
	case <-claimed:
		t.Fatal("loop scanned before readiness")
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("loop did not scan after readiness")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error=%v", err)
	}
}

func TestLoopServerSignalsReadyOnlyAfterDependencyGate(t *testing.T) {
	ready := make(chan struct{})
	called := make(chan struct{}, 1)
	s, err := NewLoopServer(loopServerStore{}, loopServerTenants{}, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{}, nil
	}, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.WaitReady = func(ctx context.Context) error {
		select {
		case <-ready:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.ReadyCheck = func() bool { return true }
	s.OnReady = func() { called <- struct{}{} }
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()
	select {
	case <-called:
		t.Fatal("ready callback fired before dependency gate")
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("ready callback did not fire after dependency gate")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error=%v", err)
	}
}

func TestLoopServerDoesNotSignalReadyWhenDomainGateFails(t *testing.T) {
	called := make(chan struct{}, 1)
	s, err := NewLoopServer(loopServerStore{}, loopServerTenants{}, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{}, nil
	}, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.ReadyCheck = func() bool { return false }
	s.OnReady = func() { called <- struct{}{} }
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()
	select {
	case <-called:
		t.Fatal("ready callback fired while domain gate was false")
	case <-time.After(30 * time.Millisecond):
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error=%v", err)
	}
}

func TestLoopServerPropagatesInvalidLoopConfiguration(t *testing.T) {
	s := &LoopServer{Loop: &work.Loop{}}
	if err := s.Start(context.Background()); err == nil {
		_ = s.Stop(context.Background())
		t.Fatal("Start hid the work loop configuration error")
	}
}

func TestLoopServerStopTimeoutKeepsRunExclusive(t *testing.T) {
	tenants := &drainingLoopTenants{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	s, err := NewLoopServer(loopServerStore{}, tenants, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{}, nil
	}, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	startResult := make(chan error, 1)
	go func() { startResult <- s.Start(context.Background()) }()
	<-tenants.started
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelStop()
	if err := s.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want deadline exceeded", err)
	}
	newCtx, cancelNew := context.WithCancel(context.Background())
	cancelNew()
	if err := s.Start(newCtx); err == nil {
		close(tenants.release)
		_ = s.Stop(context.Background())
		t.Fatal("Start accepted a new run while the previous run was still draining")
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- s.Stop(context.Background()) }()
	select {
	case err := <-stopResult:
		close(tenants.release)
		t.Fatalf("second Stop returned before the first run exited: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(tenants.release)
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want loop cancellation", err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

// This models a store call that observes cancellation but needs time to drain
// before Run actually returns; a Stop timeout must not permit overlapping runs.
type drainingLoopTenants struct {
	started, canceled, release chan struct{}
}

func (t *drainingLoopTenants) ListTenants(ctx context.Context) ([]string, error) {
	close(t.started)
	<-ctx.Done()
	close(t.canceled)
	<-t.release
	return nil, ctx.Err()
}

func TestLoopServerRejectsMissingLoop(t *testing.T) {
	if err := (&LoopServer{}).Start(context.Background()); err == nil {
		t.Fatal("Start accepted an unconfigured loop")
	}
}

func TestLoopServerStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tenants := loopServerTenants{started: make(chan struct{}), once: &sync.Once{}}
	loop := &work.Loop{
		Worker: &work.Worker{
			Store: loopServerStore{},
			Execute: func(context.Context, work.Item) (work.Result, error) {
				return work.Result{}, nil
			},
		},
		Tenants: tenants, Interval: time.Millisecond,
	}
	server := &LoopServer{Loop: loop}
	startResult := make(chan error, 1)
	go func() { startResult <- server.Start(ctx) }()
	<-tenants.started
	if err := server.Start(ctx); err == nil {
		t.Fatal("Start allowed a second run")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := server.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want cancellation", err)
	}
	if err := server.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestNewLoopServerComposesDurableDependencies(t *testing.T) {
	store := loopServerStore{}
	tenants := loopServerTenants{}
	server, err := NewLoopServer(store, tenants, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{Generation: 1}, nil
	}, time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewLoopServer() error = %v", err)
	}
	if server == nil || server.Loop == nil || server.Loop.Worker.Store == nil || server.Loop.Tenants == nil {
		t.Fatalf("NewLoopServer() did not preserve durable dependencies: %#v", server)
	}
}

func TestNewLoopServerWiresOptionalDurableRecovery(t *testing.T) {
	store := &recoveringLoopServerStore{}
	server, err := NewLoopServer(store, loopServerTenants{}, func(context.Context, work.Item) (work.Result, error) {
		return work.Result{Generation: 1}, nil
	}, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if server.Loop.Recover == nil {
		t.Fatal("NewLoopServer did not wire store recovery")
	}
	if server.Loop.Repair == nil {
		t.Fatal("NewLoopServer did not wire store repair")
	}
	if err := server.Loop.Recover(context.Background()); err != nil {
		t.Fatalf("wired recovery returned error: %v", err)
	}
	if store.recovered != 1 {
		t.Fatalf("recovery calls = %d, want 1", store.recovered)
	}
	if err := server.Loop.Repair(context.Background()); err != nil {
		t.Fatalf("wired repair returned error: %v", err)
	}
	if store.repaired != 1 {
		t.Fatalf("repair calls = %d, want 1", store.repaired)
	}
}

func TestNewLoopServerRejectsMissingDependency(t *testing.T) {
	store := loopServerStore{}
	tenants := loopServerTenants{}
	for name, args := range map[string]struct {
		store   work.Store
		tenants work.TenantSource
		execute work.Executor
	}{
		"store":    {tenants: tenants, execute: func(context.Context, work.Item) (work.Result, error) { return work.Result{}, nil }},
		"tenants":  {store: store, execute: func(context.Context, work.Item) (work.Result, error) { return work.Result{}, nil }},
		"executor": {store: store, tenants: tenants},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLoopServer(args.store, args.tenants, args.execute, 0, nil); err == nil {
				t.Fatal("NewLoopServer accepted missing dependency")
			}
		})
	}
}

type loopServerStore struct{ claim func() }

func (s loopServerStore) Claim(context.Context, string) (work.Item, bool, error) {
	if s.claim != nil {
		s.claim()
	}
	return work.Item{}, false, nil
}
func (loopServerStore) Commit(context.Context, work.Item, work.Result) error { return nil }
func (loopServerStore) Retry(context.Context, work.Item, error) error        { return nil }

type recoveringLoopServerStore struct {
	loopServerStore
	recovered, repaired int
}

func (s *recoveringLoopServerStore) Recover(context.Context) error {
	s.recovered++
	return nil
}

func (s *recoveringLoopServerStore) Repair(context.Context) error {
	s.repaired++
	return nil
}

type loopServerTenants struct {
	started chan struct{}
	once    *sync.Once
}

func (t loopServerTenants) ListTenants(context.Context) ([]string, error) {
	if t.started != nil {
		if t.once != nil {
			t.once.Do(func() { close(t.started) })
		}
	}
	return []string{"tenant"}, nil
}
