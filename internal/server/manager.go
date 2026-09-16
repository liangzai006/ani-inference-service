package server

import (
	"context"
	"errors"
	"sync"
)

// ManagerRunner is the small lifecycle surface exposed by controller-runtime
// managers. Keeping this interface local makes the Kratos adapter testable
// without constructing a Kubernetes client or starting a cluster cache.
type ManagerRunner interface {
	Start(context.Context) error
}

// ManagerServer adapts a controller-runtime manager to the Kratos server
// lifecycle. The manager is started with a child context owned by this
// adapter, so Stop can cancel it even when the adapter is tested outside a
// Kratos app. It owns no queue and does not hide manager startup errors.
type ManagerServer struct {
	Manager ManagerRunner

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
}

func (s *ManagerServer) Start(ctx context.Context) error {
	if s == nil || s.Manager == nil {
		return errors.New("controller manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("controller manager already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.started, s.cancel, s.done = true, cancel, done
	s.mu.Unlock()

	err := s.Manager.Start(runCtx)
	s.mu.Lock()
	s.err = err
	close(done)
	s.mu.Unlock()
	return err
}

func (s *ManagerServer) Stop(ctx context.Context) error {
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
	if cancel != nil {
		cancel()
	}
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
