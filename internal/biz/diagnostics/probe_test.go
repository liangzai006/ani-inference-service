package diagnostics

import (
	"context"
	"errors"
	"testing"
	"time"
)

type invocationFunc func(context.Context, Scope, string) (InvocationHealth, error)

func (f invocationFunc) ProbeInvocation(c context.Context, s Scope, m string) (InvocationHealth, error) {
	return f(c, s, m)
}
func TestProbeCacheGenerationTenantAndFailure(t *testing.T) {
	var c ProbeCache
	calls := 0
	now := time.Now().UTC()
	f := invocationFunc(func(context.Context, Scope, string) (InvocationHealth, error) {
		calls++
		return InvocationHealth{State: "healthy", ObservedAt: now}, nil
	})
	scope := Scope{TenantID: "a", ServiceID: "s", Generation: 1}
	for i := 0; i < 2; i++ {
		r, e := c.Observe(context.Background(), f, scope, "model")
		if e != nil || !r.ObservedAt.Equal(now) {
			t.Fatal(r, e)
		}
	}
	if calls != 1 {
		t.Fatal("duplicate probe", calls)
	}
	scope.Generation++
	c.Observe(context.Background(), f, scope, "model")
	scope.TenantID = "b"
	c.Observe(context.Background(), f, scope, "model")
	if calls != 3 {
		t.Fatal("probe reused across generation or tenant", calls)
	}
	scope.ServiceID = "new"
	failed := invocationFunc(func(context.Context, Scope, string) (InvocationHealth, error) {
		calls++
		return InvocationHealth{}, errors.New("source down")
	})
	for i := 0; i < 2; i++ {
		if _, e := c.Observe(context.Background(), failed, scope, "model"); e == nil {
			t.Fatal("source failure lost")
		}
	}
	if calls != 5 {
		t.Fatal("failed probe cached")
	}
}
