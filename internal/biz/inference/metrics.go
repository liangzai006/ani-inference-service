package inference

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Model materialization metrics deliberately carry no tenant, service,
// operation, model or request identifiers. Those dimensions belong in
// traces/logs; keeping them out of metrics prevents unbounded cardinality.
var modelMetrics struct {
	sync.Once
	loadDuration metric.Float64Histogram
	errors       metric.Int64Counter
}

func initModelMetrics() {
	meter := otel.Meter("ani-inference-service")
	modelMetrics.loadDuration, _ = meter.Float64Histogram(
		"ani_model_materialization_duration_seconds",
		metric.WithDescription("Time spent in model materialization provider attempts."),
	)
	modelMetrics.errors, _ = meter.Int64Counter(
		"ani_model_materialization_error_total",
		metric.WithDescription("Model materialization attempts that did not establish readiness."),
	)
}

func recordModelMaterialization(ctx context.Context, duration time.Duration, err error) {
	modelMetrics.Do(initModelMetrics)
	modelMetrics.loadDuration.Record(ctx, duration.Seconds())
	if err != nil {
		modelMetrics.errors.Add(ctx, 1, metric.WithAttributes(attribute.String("error_class", modelErrorClass(err))))
	}
}

func modelErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "context"
	case errors.Is(err, ErrOperationProviderMissing):
		return "provider_missing"
	case errors.Is(err, ErrOperationNotReady):
		return "not_ready"
	default:
		return "provider_or_domain"
	}
}
