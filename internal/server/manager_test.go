package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeManager struct {
	started chan struct{}
	stopped chan struct{}
	err     error
}

func (m *fakeManager) Start(ctx context.Context) error {
	close(m.started)
	<-ctx.Done()
	close(m.stopped)
	if m.err != nil {
		return m.err
	}
	return ctx.Err()
}

func TestManagerServerRequiresManager(t *testing.T) {
	if err := (&ManagerServer{}).Start(context.Background()); err == nil {
		t.Fatal("Start accepted an unconfigured manager")
	}
}

func TestManagerServerCancelsAndWaits(t *testing.T) {
	m := &fakeManager{started: make(chan struct{}), stopped: make(chan struct{})}
	s := &ManagerServer{Manager: m}
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(context.Background()) }()
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("manager did not start")
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-m.stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop returned before manager stopped")
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context cancellation", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

func TestManagerServerPropagatesManagerError(t *testing.T) {
	want := errors.New("manager failed")
	m := &fakeManager{started: make(chan struct{}), stopped: make(chan struct{}), err: want}
	s := &ManagerServer{Manager: m}
	ctx, cancel := context.WithCancel(context.Background())
	startErr := make(chan error, 1)
	go func() { startErr <- s.Start(ctx) }()
	<-m.started
	cancel()
	if err := <-startErr; !errors.Is(err, want) {
		t.Fatalf("Start() error = %v, want %v", err, want)
	}
	if err := s.Stop(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Stop() error = %v, want %v", err, want)
	}
}

func TestManagerServerStopTimeoutKeepsRunExclusive(t *testing.T) {
	m := &drainingManager{started: make(chan struct{}), release: make(chan struct{})}
	s := &ManagerServer{Manager: m}
	startResult := make(chan error, 1)
	go func() { startResult <- s.Start(context.Background()) }()
	<-m.started
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelStop()
	if err := s.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop() error = %v, want deadline exceeded", err)
	}
	newCtx, cancelNew := context.WithCancel(context.Background())
	cancelNew()
	if err := s.Start(newCtx); err == nil {
		close(m.release)
		_ = s.Stop(context.Background())
		t.Fatal("Start accepted a new run while the previous manager was still draining")
	}
	stopResult := make(chan error, 1)
	go func() { stopResult <- s.Stop(context.Background()) }()
	select {
	case err := <-stopResult:
		close(m.release)
		t.Fatalf("second Stop returned before manager exited: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(m.release)
	if err := <-startResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want cancellation", err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

type drainingManager struct {
	started, release chan struct{}
}

func (m *drainingManager) Start(ctx context.Context) error {
	close(m.started)
	<-ctx.Done()
	<-m.release
	return ctx.Err()
}
