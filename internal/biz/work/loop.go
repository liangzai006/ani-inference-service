package work

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// TenantSource is the explicit platform entry used by the background worker
// to discover tenant scopes. It is separate from tenant-scoped business
// queries; implementations must still pass each returned tenant to Claim.
type TenantSource interface {
	ListTenants(context.Context) ([]string, error)
}

// Loop periodically lets PostgreSQL leases decide which durable work is
// executable. The loop is only a scheduler; it never stores work in memory.
type Loop struct {
	Worker   *Worker
	Tenants  TenantSource
	Interval time.Duration
	OnError  func(error)
	// Recover repairs durable work rows from the service aggregate. It is
	// called before the first pass and periodically thereafter; events and
	// resource_work rows are hints, so a complete persisted scan is required
	// to recover rows lost while the process was running.
	Recover func(context.Context) error
	// Repair is the cheaper periodic variant of Recover. When present it only
	// recreates missing durable work rows and preserves existing notifications.
	Repair           func(context.Context) error
	RecoveryInterval time.Duration
}

// RunOnce performs one best-effort pass over all tenant scopes. A failed
// tenant does not prevent other tenants from being attempted; the error is
// returned so the caller can record it and the next tick can retry it.
func (l *Loop) RunOnce(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tenants, err := l.Tenants.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("durable work list tenants: %w", err)
	}
	var joined error
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		if tenant == "" {
			continue
		}
		if err := l.Worker.RunOnce(ctx, tenant); err != nil {
			joined = errors.Join(joined, fmt.Errorf("durable work tenant %q: %w", tenant, err))
		}
	}
	return errors.Join(joined, ctx.Err())
}

func (l *Loop) validate() error {
	if l == nil || l.Worker == nil || l.Worker.Store == nil {
		return errors.New("durable work loop worker is nil")
	}
	if l.Worker.Execute == nil {
		return errors.New("durable work loop executor is nil")
	}
	if l.Tenants == nil {
		return errors.New("durable work loop tenant source is nil")
	}
	return nil
}

// Run keeps retrying from PostgreSQL until the process is stopped. A failed
// pass is deliberately not fatal: the next tick retries after dependencies or
// leases recover. The default interval is intentionally bounded and explicit.
func (l *Loop) Run(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := l.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	recoveryInterval := l.RecoveryInterval
	if recoveryInterval <= 0 {
		recoveryInterval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastRecovery time.Time
	for {
		if (l.Recover != nil || l.Repair != nil) && (lastRecovery.IsZero() || time.Since(lastRecovery) >= recoveryInterval) {
			startupRecovery := lastRecovery.IsZero()
			// Record the attempt even when the dependency is temporarily down;
			// this bounds retries and lets the normal loop continue processing
			// already durable rows. The next recovery interval retries repair.
			lastRecovery = time.Now()
			recovery := l.Recover
			if !startupRecovery && l.Repair != nil {
				recovery = l.Repair
			}
			if recovery != nil {
				if err := recovery(ctx); err != nil && l.OnError != nil {
					l.OnError(fmt.Errorf("durable work recovery: %w", err))
				}
			}
		}
		err := l.RunOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && l.OnError != nil {
			l.OnError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
