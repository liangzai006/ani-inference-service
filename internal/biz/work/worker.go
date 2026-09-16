// Package work implements the durable-work execution boundary. The store is
// backed by PostgreSQL in production; the worker never treats an in-memory
// queue as a source of truth.
package work

import (
	"context"
	"errors"
	"time"
)

var (
	ErrStaleGeneration = errors.New("stale generation")
	ErrLeaseLost       = errors.New("work lease expired, superseded, or generation changed")
)

type Item struct {
	TenantID, ServiceID string
	Generation          int64
	DirtyVersion        int64
	LeaseToken          string
}
type Result struct{ Generation int64 }

type Store interface {
	Claim(context.Context, string) (Item, bool, error)
	Commit(context.Context, Item, Result) error
	Retry(context.Context, Item, error) error
}

// MetricsSnapshot is a bounded operational snapshot. It contains no tenant,
// service or operation identifiers and is intended for low-cardinality gauges.
type MetricsSnapshot struct {
	DueWork           int64
	OldestWorkAge     time.Duration
	StaleObservations int64
}

// MetricsSource is optional and read-only. Implementations must query durable
// state; in-memory queues are not a source for these gauges.
type MetricsSource interface {
	MetricsSnapshot(context.Context) (MetricsSnapshot, error)
}
type Executor func(context.Context, Item) (Result, error)

type Worker struct {
	Store   Store
	Execute Executor
}

func (w Worker) RunOnce(ctx context.Context, tenantID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	item, ok, err := w.Store.Claim(ctx, tenantID)
	if err != nil {
		recordFailure(ctx, err)
		return err
	}
	if !ok {
		recordEmpty(ctx)
		return nil
	}
	recordClaim(ctx)
	result, err := w.Execute(ctx, item)
	if err != nil {
		recordRetry(ctx, err)
		if retryErr := w.Store.Retry(ctx, item, err); retryErr != nil {
			recordFailure(ctx, retryErr)
			return retryErr
		}
		return err
	}
	if result.Generation != item.Generation {
		recordFailure(ctx, ErrStaleGeneration)
		return ErrStaleGeneration
	}
	if err := w.Store.Commit(ctx, item, result); err != nil {
		recordFailure(ctx, err)
		return err
	}
	recordCompleted(ctx)
	return nil
}
