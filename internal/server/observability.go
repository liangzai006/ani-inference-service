package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	kratosmetrics "github.com/go-kratos/kratos/contrib/otel/v3/metrics"
	kratostracing "github.com/go-kratos/kratos/contrib/otel/v3/tracing"
	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/logging"
	"github.com/go-kratos/kratos/v3/middleware/metadata"
	"github.com/go-kratos/kratos/v3/middleware/recovery"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
	"go.opentelemetry.io/otel"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	metricSdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	traceSdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type Observability struct {
	registry       *prometheus.Registry
	meterProvider  *metricSdk.MeterProvider
	tracerProvider *traceSdk.TracerProvider
	requests       metric.Int64Counter
	seconds        metric.Float64Histogram
	metricsMu      sync.RWMutex
	metricsSource  work.MetricsSource
}

func NewObservability(name, version string, readiness *Readiness) (*Observability, error) {
	if err := kratosmetrics.EnableOTELExemplar(); err != nil {
		return nil, fmt.Errorf("enable OpenTelemetry exemplars: %w", err)
	}

	registry := prometheus.NewRegistry()
	if err := registry.Register(prometheus.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("register Go collector: %w", err)
	}
	if err := registry.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("register process collector: %w", err)
	}
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("create OpenTelemetry Prometheus exporter: %w", err)
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(name),
		semconv.ServiceVersion(version),
	)
	meterProvider := metricSdk.NewMeterProvider(
		metricSdk.WithResource(res),
		metricSdk.WithReader(exporter),
		metricSdk.WithView(kratosmetrics.DefaultSecondsHistogramView(kratosmetrics.DefaultServerSecondsHistogramName)),
	)
	tracerProvider := traceSdk.NewTracerProvider(
		traceSdk.WithResource(res),
		traceSdk.WithSampler(traceSdk.ParentBased(traceSdk.AlwaysSample())),
	)

	meter := meterProvider.Meter(name)
	requests, err := kratosmetrics.DefaultRequestsCounter(meter, kratosmetrics.DefaultServerRequestsCounterName)
	if err != nil {
		return nil, fmt.Errorf("create Kratos server request counter: %w", err)
	}
	seconds, err := kratosmetrics.DefaultSecondsHistogram(meter, kratosmetrics.DefaultServerSecondsHistogramName)
	if err != nil {
		return nil, fmt.Errorf("create Kratos server latency histogram: %w", err)
	}
	obs := &Observability{
		registry: registry, meterProvider: meterProvider, tracerProvider: tracerProvider,
		requests: requests, seconds: seconds,
	}
	if _, err := meter.Int64ObservableGauge(
		"ani_runtime_ready",
		metric.WithDescription("Whether this process completed its local runtime start hook."),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			if readiness.Ready() {
				observer.Observe(1)
			} else {
				observer.Observe(0)
			}
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("create readiness gauge: %w", err)
	}
	if _, err := meter.Int64ObservableGauge(
		"ani_work_due",
		metric.WithDescription("Durable work items currently due for execution."),
		metric.WithInt64Callback(func(ctx context.Context, observer metric.Int64Observer) error {
			source := obs.getMetricsSource()
			if source == nil {
				return nil
			}
			snapshot, err := source.MetricsSnapshot(ctx)
			if err != nil {
				return err
			}
			observer.Observe(snapshot.DueWork)
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("create durable work gauge: %w", err)
	}
	if _, err := meter.Float64ObservableGauge(
		"ani_work_oldest_age_seconds",
		metric.WithDescription("Age in seconds of the oldest due durable work item."),
		metric.WithFloat64Callback(func(ctx context.Context, observer metric.Float64Observer) error {
			source := obs.getMetricsSource()
			if source == nil {
				return nil
			}
			snapshot, err := source.MetricsSnapshot(ctx)
			if err != nil {
				return err
			}
			observer.Observe(snapshot.OldestWorkAge.Seconds())
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("create oldest work age gauge: %w", err)
	}
	if _, err := meter.Int64ObservableGauge(
		"ani_observation_stale",
		metric.WithDescription("Runtime observations past their durable freshness deadline."),
		metric.WithInt64Callback(func(ctx context.Context, observer metric.Int64Observer) error {
			source := obs.getMetricsSource()
			if source == nil {
				return nil
			}
			snapshot, err := source.MetricsSnapshot(ctx)
			if err != nil {
				return err
			}
			observer.Observe(snapshot.StaleObservations)
			return nil
		}),
	); err != nil {
		return nil, fmt.Errorf("create stale observation gauge: %w", err)
	}
	otel.SetMeterProvider(meterProvider)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return obs, nil
}

func (o *Observability) SetMetricsSource(source work.MetricsSource) {
	if o == nil {
		return
	}
	o.metricsMu.Lock()
	o.metricsSource = source
	o.metricsMu.Unlock()
}

func (o *Observability) getMetricsSource() work.MetricsSource {
	if o == nil {
		return nil
	}
	o.metricsMu.RLock()
	defer o.metricsMu.RUnlock()
	return o.metricsSource
}

func (o *Observability) Gatherer() prometheus.Gatherer {
	return o.registry
}

func (o *Observability) ServerMiddleware(logger *slog.Logger) []middleware.Middleware {
	return []middleware.Middleware{
		recovery.Recovery(recovery.WithLogger(logger)),
		metadata.Server(),
		kratostracing.Server(kratostracing.WithTracerProvider(o.tracerProvider)),
		logging.Server(logger),
		kratosmetrics.Server(
			kratosmetrics.WithRequests(o.requests),
			kratosmetrics.WithSeconds(o.seconds),
		),
		validate.Validator(),
	}
}

func (o *Observability) Shutdown(ctx context.Context) error {
	return errors.Join(
		o.meterProvider.Shutdown(ctx),
		o.tracerProvider.Shutdown(ctx),
	)
}
