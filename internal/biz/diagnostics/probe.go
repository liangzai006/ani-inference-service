package diagnostics

import (
	"context"
	"sync"
	"time"
)

type InvocationHealth struct {
	State, Reason string
	ObservedAt    time.Time
}

// Probe performs one small generation against the bound runtime. It does not
// claim to validate the caller's gateway credentials or service access policy.
type InvocationProber interface {
	ProbeInvocation(context.Context, Scope, string) (InvocationHealth, error)
}

type probeEntry struct {
	health  InvocationHealth
	expires time.Time
}
type ProbeCache struct {
	mu      sync.Mutex
	entries map[Scope]probeEntry
	active  map[Scope]bool
}

func (c *ProbeCache) Observe(ctx context.Context, prober InvocationProber, scope Scope, model string) (InvocationHealth, error) {
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.entries[scope]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return entry.health, nil
	}
	if c.active[scope] || len(c.active) >= 4 {
		c.mu.Unlock()
		return InvocationHealth{State: "unknown", Reason: "invocation_probe_in_progress"}, nil
	}
	if c.entries == nil {
		c.entries = make(map[Scope]probeEntry)
		c.active = make(map[Scope]bool)
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) >= 1024 {
		c.mu.Unlock()
		return InvocationHealth{State: "unknown", Reason: "invocation_probe_capacity"}, nil
	}
	c.active[scope] = true
	c.mu.Unlock()
	result, err := prober.ProbeInvocation(ctx, scope, model)
	c.mu.Lock()
	delete(c.active, scope)
	if err == nil && result.State != "unknown" {
		c.entries[scope] = probeEntry{result, time.Now().Add(time.Minute)}
	}
	c.mu.Unlock()
	return result, err
}
