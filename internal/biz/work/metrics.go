package work

import (
	"context"
	"errors"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Worker metrics intentionally use only bounded outcome/error labels. Tenant,
// service, operation and request identifiers belong in traces/logs, not metric
// cardinality.
var workerMetrics struct {
	sync.Once
	claimed, empty, completed, retried, failed metric.Int64Counter
}

func initWorkerMetrics() {
	meter := otel.Meter("ani-inference-service")
	workerMetrics.claimed, _ = meter.Int64Counter("ani_work_claim_total", metric.WithDescription("Durable work items successfully claimed."))
	workerMetrics.empty, _ = meter.Int64Counter("ani_work_claim_empty_total", metric.WithDescription("Durable work scans with no claim."))
	workerMetrics.completed, _ = meter.Int64Counter("ani_work_completed_total", metric.WithDescription("Durable work items committed successfully."))
	workerMetrics.retried, _ = meter.Int64Counter("ani_work_retry_total", metric.WithDescription("Durable work executions scheduled for retry."))
	workerMetrics.failed, _ = meter.Int64Counter("ani_work_failure_total", metric.WithDescription("Durable work failures that could not be committed or retried."))
}

func recordClaim(ctx context.Context) {
	workerMetrics.Do(initWorkerMetrics)
	workerMetrics.claimed.Add(ctx, 1)
}

func recordEmpty(ctx context.Context) {
	workerMetrics.Do(initWorkerMetrics)
	workerMetrics.empty.Add(ctx, 1)
}

func recordCompleted(ctx context.Context) {
	workerMetrics.Do(initWorkerMetrics)
	workerMetrics.completed.Add(ctx, 1)
}

func recordRetry(ctx context.Context, err error) {
	workerMetrics.Do(initWorkerMetrics)
	workerMetrics.retried.Add(ctx, 1, metric.WithAttributes(attribute.String("error_class", errorClass(err))))
}

func recordFailure(ctx context.Context, err error) {
	workerMetrics.Do(initWorkerMetrics)
	workerMetrics.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("error_class", errorClass(err))))
}

func errorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrStaleGeneration):
		return "stale_generation"
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "context"
	default:
		return "dependency_or_domain"
	}
}
