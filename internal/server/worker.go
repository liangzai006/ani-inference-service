package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

// LoopServer adapts the durable PostgreSQL-backed work loop to the Kratos
// server lifecycle. Start blocks until the loop exits, allowing Kratos to
// observe configuration and runtime errors. Stop cancels the child context and
// waits for the loop to finish. The loop itself remains the source of truth:
// this adapter owns no in-memory task queue.
type LoopServer struct {
	Loop *work.Loop
	// WaitReady gates the first durable scan on an external dependency such as
	// the controller-runtime cache. It is optional for isolated unit tests.
	WaitReady func(context.Context) error
	// ReadyCheck is evaluated after WaitReady succeeds. It lets composition
	// keep readiness false while durable infrastructure is available but
	// required domain providers are not wired yet.
	ReadyCheck func() bool
	// OnReady is called once the dependency gate has passed. It is a lifecycle
	// callback only; it does not carry work or state.
	OnReady func()

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

// MetricsSource exposes the loop's durable store for aggregate operational
// gauges. It returns no tenant or resource identifiers.
func (s *LoopServer) MetricsSource() work.MetricsSource {
	if s == nil || s.Loop == nil || s.Loop.Worker == nil {
		return nil
	}
	source, _ := s.Loop.Worker.Store.(work.MetricsSource)
	return source
}

// NewLoopServer composes the durable worker from its explicit persistence and
// execution ports. The constructor performs no background work; callers may
// pass the returned server to kratos.Server and let the application own its
// lifecycle. Work remains in the supplied Store (normally PostgreSQL), never
// in this adapter.
func NewLoopServer(store work.Store, tenants work.TenantSource, execute work.Executor, interval time.Duration, onError func(error)) (*LoopServer, error) {
	if store == nil {
		return nil, errors.New("durable work store is not configured")
	}
	if tenants == nil {
		return nil, errors.New("durable work tenant source is not configured")
	}
	if execute == nil {
		return nil, errors.New("durable work executor is not configured")
	}
	loop := &work.Loop{
		Worker:   &work.Worker{Store: store, Execute: execute},
		Tenants:  tenants,
		Interval: interval,
		OnError:  onError,
	}
	// PostgreSQL WorkStore exposes Recover as an optional capability. Wiring it
	// here keeps startup and periodic repair part of the lifecycle contract;
	// callers cannot accidentally omit recovery when composing the server.
	if recoverer, ok := store.(interface{ Recover(context.Context) error }); ok {
		loop.Recover = recoverer.Recover
	}
	if repairer, ok := store.(interface{ Repair(context.Context) error }); ok {
		loop.Repair = repairer.Repair
	}
	return &LoopServer{Loop: loop}, nil
}

// SetReadyCallback lets the owning application connect dependency readiness
// to its admin readyz endpoint without coupling this worker to HTTP or Kratos.
func (s *LoopServer) SetReadyCallback(callback func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.OnReady = callback
	s.mu.Unlock()
}

func (s *LoopServer) Start(ctx context.Context) error {
	if s == nil || s.Loop == nil {
		return errors.New("durable work loop server is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("durable work loop server already started")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.started, s.cancel, s.done = true, cancel, done
	s.mu.Unlock()

	if s.WaitReady != nil {
		if err := s.WaitReady(loopCtx); err != nil {
			s.mu.Lock()
			s.err = err
			close(done)
			s.mu.Unlock()
			return err
		}
	}
	if s.ReadyCheck == nil || s.ReadyCheck() {
		if s.OnReady != nil {
			s.OnReady()
		}
	}
	err := s.Loop.Run(loopCtx)
	s.mu.Lock()
	s.err = err
	close(done)
	s.mu.Unlock()
	return err
}

func (s *LoopServer) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
