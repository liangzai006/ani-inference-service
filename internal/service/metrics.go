package service

import (
	"context"
	"fmt"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

// MetricsCollector gathers metrics from various sources
type MetricsCollector struct {
	// PrometheusClient is optional - if nil, returns simulated data
	PrometheusClient *PrometheusClient
}

// NewMetricsCollector creates a new MetricsCollector
// If prometheusAddress is empty, collector will return simulated data
func NewMetricsCollector(prometheusAddress, namespace string) *MetricsCollector {
	collector := &MetricsCollector{}

	if prometheusAddress != "" {
		promClient, err := NewPrometheusClient(prometheusAddress, namespace)
		if err == nil {
			collector.PrometheusClient = promClient
		}
		// If error, PrometheusClient remains nil and simulated data will be used
	}

	return collector
}

// CollectServiceMetrics gathers comprehensive metrics for an inference service
// tenantID should be extracted from context before calling this method
func (m *MetricsCollector) CollectServiceMetrics(ctx context.Context, tenantID string, svc *inferencev1.InferenceService) (*inferencev1.GetServiceMetricsResponse, error) {
	// If Prometheus client is configured, use real data
	if m.PrometheusClient != nil {
		return m.PrometheusClient.QueryMetrics(ctx, tenantID, svc.Id)
	}

	// Otherwise, return simulated data based on service state
	return m.collectSimulatedMetrics(svc), nil
}

// collectSimulatedMetrics returns simulated metrics for testing/development
func (m *MetricsCollector) collectSimulatedMetrics(svc *inferencev1.InferenceService) *inferencev1.GetServiceMetricsResponse {
	metrics := &inferencev1.GetServiceMetricsResponse{
		Latency: &inferencev1.LatencyStats{},
		ResourceUsage: &inferencev1.ResourceUsage{
			Cpu:    &inferencev1.CpuUsage{},
			Memory: &inferencev1.MemoryUsage{},
		},
	}

	// Simulate metrics based on service health
	if svc.InvocationHealth == "healthy" {
		metrics.TotalRequests = 15420
		metrics.SuccessfulRequests = 15389
		metrics.FailedRequests = 31
		metrics.SuccessRate = 99.8

		metrics.Latency.P50Ms = 85.3
		metrics.Latency.P95Ms = 142.7
		metrics.Latency.P99Ms = 203.4
		metrics.Latency.MaxMs = 250.0
	} else if svc.InvocationHealth == "unhealthy" {
		metrics.TotalRequests = 8230
		metrics.SuccessfulRequests = 6920
		metrics.FailedRequests = 1310
		metrics.SuccessRate = 84.1

		metrics.Latency.P50Ms = 320.5
		metrics.Latency.P95Ms = 1842.3
		metrics.Latency.P99Ms = 4203.7
		metrics.Latency.MaxMs = 5000.0
	}

	// Add resource usage metrics
	metrics.ResourceUsage.Cpu.AverageCores = 2.4
	metrics.ResourceUsage.Cpu.PeakCores = 3.8

	metrics.ResourceUsage.Memory.AverageBytes = 6_442_450_944 // ~6 GB
	metrics.ResourceUsage.Memory.PeakBytes = 8_589_934_592    // ~8 GB

	// Add GPU metrics if the service uses GPUs
	if svc.Resource != nil && svc.Resource.Gpu != nil && svc.Resource.Gpu.DevicesPerReplica > 0 {
		metrics.ResourceUsage.Gpu = &inferencev1.GpuUsage{
			AverageUtilizationPercent: 75.5,
			PeakUtilizationPercent:    92.3,
			AverageMemoryBytes:        13_958_643_712, // ~13 GB
			PeakMemoryBytes:           15_728_640_000, // ~15 GB
		}
	}

	return metrics
}

// PrometheusQueryBuilder builds PromQL queries for metrics collection
type PrometheusQueryBuilder struct {
	ServiceID string
	TenantID  string
	TimeRange string // e.g., "5m", "1h"
}

// BuildRequestRateQuery returns PromQL for request rate
func (b *PrometheusQueryBuilder) BuildRequestRateQuery() string {
	return fmt.Sprintf(
		`sum(rate(inference_requests_total{service_id="%s",tenant_id="%s"}[%s]))`,
		b.ServiceID, b.TenantID, b.TimeRange,
	)
}

// BuildSuccessRateQuery returns PromQL for success rate percentage
func (b *PrometheusQueryBuilder) BuildSuccessRateQuery() string {
	return fmt.Sprintf(
		`sum(rate(inference_requests_total{service_id="%s",tenant_id="%s",status="success"}[%s])) / sum(rate(inference_requests_total{service_id="%s",tenant_id="%s"}[%s])) * 100`,
		b.ServiceID, b.TenantID, b.TimeRange,
		b.ServiceID, b.TenantID, b.TimeRange,
	)
}

// BuildLatencyP95Query returns PromQL for P95 latency
func (b *PrometheusQueryBuilder) BuildLatencyP95Query() string {
	return fmt.Sprintf(
		`histogram_quantile(0.95, sum(rate(inference_request_duration_seconds_bucket{service_id="%s",tenant_id="%s"}[%s])) by (le))`,
		b.ServiceID, b.TenantID, b.TimeRange,
	)
}

// BuildLatencyP99Query returns PromQL for P99 latency
func (b *PrometheusQueryBuilder) BuildLatencyP99Query() string {
	return fmt.Sprintf(
		`histogram_quantile(0.99, sum(rate(inference_request_duration_seconds_bucket{service_id="%s",tenant_id="%s"}[%s])) by (le))`,
		b.ServiceID, b.TenantID, b.TimeRange,
	)
}

// BuildCPUUsageQuery returns PromQL for CPU usage
func (b *PrometheusQueryBuilder) BuildCPUUsageQuery(namespace, serviceName string) string {
	return fmt.Sprintf(
		`sum(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*"}[%s]))`,
		namespace, serviceName, b.TimeRange,
	)
}

// BuildMemoryUsageQuery returns PromQL for memory usage
func (b *PrometheusQueryBuilder) BuildMemoryUsageQuery(namespace, serviceName string) string {
	return fmt.Sprintf(
		`sum(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*"})`,
		namespace, serviceName,
	)
}

// BuildGPUUtilizationQuery returns PromQL for GPU utilization
func (b *PrometheusQueryBuilder) BuildGPUUtilizationQuery(namespace, serviceName string) string {
	return fmt.Sprintf(
		`avg(nvidia_gpu_duty_cycle{namespace="%s",pod=~"%s-.*"})`,
		namespace, serviceName,
	)
}

// BuildGPUMemoryQuery returns PromQL for GPU memory usage
func (b *PrometheusQueryBuilder) BuildGPUMemoryQuery(namespace, serviceName string) string {
	return fmt.Sprintf(
		`sum(nvidia_gpu_memory_used_bytes{namespace="%s",pod=~"%s-.*"})`,
		namespace, serviceName,
	)
}
