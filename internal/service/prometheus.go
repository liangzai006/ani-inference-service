package service

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

// PrometheusClient wraps the Prometheus API client
type PrometheusClient struct {
	API       promv1.API
	Namespace string // Kubernetes namespace for inference services
}

// NewPrometheusClient creates a new Prometheus client
func NewPrometheusClient(address, namespace string) (*PrometheusClient, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid Prometheus address %q", address)
	}
	client, err := api.NewClient(api.Config{
		Address: address,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating Prometheus client: %w", err)
	}

	return &PrometheusClient{
		API:       promv1.NewAPI(client),
		Namespace: namespace,
	}, nil
}

// QueryMetrics queries Prometheus for all relevant metrics for a service
func (p *PrometheusClient) QueryMetrics(ctx context.Context, tenantID, serviceID string) (*inferencev1.GetServiceMetricsResponse, error) {
	metrics := &inferencev1.GetServiceMetricsResponse{
		Latency: &inferencev1.LatencyStats{},
		ResourceUsage: &inferencev1.ResourceUsage{
			Cpu:    &inferencev1.CpuUsage{},
			Memory: &inferencev1.MemoryUsage{},
		},
	}

	// Query request metrics
	if err := p.queryRequestMetrics(ctx, tenantID, serviceID, metrics); err != nil {
		return nil, fmt.Errorf("error querying request metrics: %w", err)
	}

	// Query latency metrics
	if err := p.queryLatencyMetrics(ctx, tenantID, serviceID, metrics); err != nil {
		return nil, fmt.Errorf("error querying latency metrics: %w", err)
	}

	// Query resource metrics
	if err := p.queryResourceMetrics(ctx, tenantID, serviceID, metrics); err != nil {
		return nil, fmt.Errorf("error querying resource metrics: %w", err)
	}

	return metrics, nil
}

// queryRequestMetrics queries total requests, success rate, etc.
func (p *PrometheusClient) queryRequestMetrics(ctx context.Context, tenantID, serviceID string, metrics *inferencev1.GetServiceMetricsResponse) error {
	timeRange := "5m"

	// Total requests rate (requests per second over 5 minutes)
	totalQuery := fmt.Sprintf(
		`sum(rate(inference_requests_total{tenant_id="%s",service_id="%s",namespace="%s"}[%s]))`,
		tenantID, serviceID, p.Namespace, timeRange,
	)
	totalRate, err := p.queryScalar(ctx, totalQuery)
	if err != nil {
		return fmt.Errorf("error querying total requests: %w", err)
	}

	// Estimate total requests (rate * 5 minutes * 60 seconds)
	metrics.TotalRequests = int64(totalRate * 300)

	// Success requests rate
	successQuery := fmt.Sprintf(
		`sum(rate(inference_requests_total{tenant_id="%s",service_id="%s",namespace="%s",status="success"}[%s]))`,
		tenantID, serviceID, p.Namespace, timeRange,
	)
	successRate, err := p.queryScalar(ctx, successQuery)
	if err != nil {
		return fmt.Errorf("error querying success requests: %w", err)
	}

	metrics.SuccessfulRequests = int64(successRate * 300)
	metrics.FailedRequests = metrics.TotalRequests - metrics.SuccessfulRequests

	// Calculate success rate percentage
	if metrics.TotalRequests > 0 {
		metrics.SuccessRate = float64(metrics.SuccessfulRequests) / float64(metrics.TotalRequests) * 100
	}

	return nil
}

// queryLatencyMetrics queries P50, P95, P99 latencies
func (p *PrometheusClient) queryLatencyMetrics(ctx context.Context, tenantID, serviceID string, metrics *inferencev1.GetServiceMetricsResponse) error {
	timeRange := "5m"
	baseQuery := fmt.Sprintf(
		`histogram_quantile(%%s, sum(rate(inference_request_duration_seconds_bucket{tenant_id="%s",service_id="%s",namespace="%s"}[%s])) by (le))`,
		tenantID, serviceID, p.Namespace, timeRange,
	)

	// Query P50 (median)
	p50, err := p.queryScalar(ctx, fmt.Sprintf(baseQuery, "0.50"))
	if err == nil {
		metrics.Latency.P50Ms = p50 * 1000 // Convert to milliseconds
	}

	// Query P95
	p95, err := p.queryScalar(ctx, fmt.Sprintf(baseQuery, "0.95"))
	if err == nil {
		metrics.Latency.P95Ms = p95 * 1000
	}

	// Query P99
	p99, err := p.queryScalar(ctx, fmt.Sprintf(baseQuery, "0.99"))
	if err == nil {
		metrics.Latency.P99Ms = p99 * 1000
	}

	// Query max latency
	maxQuery := fmt.Sprintf(
		`max(inference_request_duration_seconds{tenant_id="%s",service_id="%s",namespace="%s"})`,
		tenantID, serviceID, p.Namespace,
	)
	maxLatency, err := p.queryScalar(ctx, maxQuery)
	if err == nil {
		metrics.Latency.MaxMs = maxLatency * 1000
	}

	return nil
}

// queryResourceMetrics queries CPU, memory, and GPU usage
func (p *PrometheusClient) queryResourceMetrics(ctx context.Context, tenantID, serviceID string, metrics *inferencev1.GetServiceMetricsResponse) error {
	podSelector := fmt.Sprintf("%s-%s", tenantID, serviceID)
	timeRange := "5m"

	// CPU usage (average cores over 5 minutes)
	cpuQuery := fmt.Sprintf(
		`avg(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*",container!=""}[%s]))`,
		p.Namespace, podSelector, timeRange,
	)
	avgCPU, err := p.queryScalar(ctx, cpuQuery)
	if err == nil {
		metrics.ResourceUsage.Cpu.AverageCores = avgCPU
	}

	// Peak CPU (max over 5 minutes)
	peakCPUQuery := fmt.Sprintf(
		`max(rate(container_cpu_usage_seconds_total{namespace="%s",pod=~"%s-.*",container!=""}[%s]))`,
		p.Namespace, podSelector, timeRange,
	)
	peakCPU, err := p.queryScalar(ctx, peakCPUQuery)
	if err == nil {
		metrics.ResourceUsage.Cpu.PeakCores = peakCPU
	}

	// Average memory usage
	memQuery := fmt.Sprintf(
		`avg(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*",container!=""})`,
		p.Namespace, podSelector,
	)
	avgMem, err := p.queryScalar(ctx, memQuery)
	if err == nil {
		metrics.ResourceUsage.Memory.AverageBytes = int64(avgMem)
	}

	// Peak memory usage
	peakMemQuery := fmt.Sprintf(
		`max(container_memory_working_set_bytes{namespace="%s",pod=~"%s-.*",container!=""})`,
		p.Namespace, podSelector,
	)
	peakMem, err := p.queryScalar(ctx, peakMemQuery)
	if err == nil {
		metrics.ResourceUsage.Memory.PeakBytes = int64(peakMem)
	}

	// Query GPU metrics if available
	if err := p.queryGPUMetrics(ctx, podSelector, metrics); err == nil {
		// GPU metrics were available and populated
	}

	return nil
}

// queryGPUMetrics queries GPU utilization and memory
func (p *PrometheusClient) queryGPUMetrics(ctx context.Context, podSelector string, metrics *inferencev1.GetServiceMetricsResponse) error {
	timeRange := "5m"

	// Check if GPU metrics exist (using DCGM exporter metrics)
	gpuUtilQuery := fmt.Sprintf(
		`avg(DCGM_FI_DEV_GPU_UTIL{namespace="%s",pod=~"%s-.*"})`,
		p.Namespace, podSelector,
	)
	avgGPUUtil, err := p.queryScalar(ctx, gpuUtilQuery)
	if err != nil {
		// No GPU metrics available
		return err
	}

	// Initialize GPU usage struct
	metrics.ResourceUsage.Gpu = &inferencev1.GpuUsage{}

	// Average GPU utilization
	metrics.ResourceUsage.Gpu.AverageUtilizationPercent = avgGPUUtil

	// Peak GPU utilization
	peakGPUQuery := fmt.Sprintf(
		`max_over_time(DCGM_FI_DEV_GPU_UTIL{namespace="%s",pod=~"%s-.*"}[%s])`,
		p.Namespace, podSelector, timeRange,
	)
	peakGPUUtil, err := p.queryScalar(ctx, peakGPUQuery)
	if err == nil {
		metrics.ResourceUsage.Gpu.PeakUtilizationPercent = peakGPUUtil
	}

	// Average GPU memory used
	avgGPUMemQuery := fmt.Sprintf(
		`avg(DCGM_FI_DEV_FB_USED{namespace="%s",pod=~"%s-.*"}) * 1024 * 1024`,
		p.Namespace, podSelector,
	)
	avgGPUMem, err := p.queryScalar(ctx, avgGPUMemQuery)
	if err == nil {
		metrics.ResourceUsage.Gpu.AverageMemoryBytes = int64(avgGPUMem)
	}

	// Peak GPU memory
	peakGPUMemQuery := fmt.Sprintf(
		`max_over_time(DCGM_FI_DEV_FB_USED{namespace="%s",pod=~"%s-.*"}[%s]) * 1024 * 1024`,
		p.Namespace, podSelector, timeRange,
	)
	peakGPUMem, err := p.queryScalar(ctx, peakGPUMemQuery)
	if err == nil {
		metrics.ResourceUsage.Gpu.PeakMemoryBytes = int64(peakGPUMem)
	}

	return nil
}

// queryScalar executes a PromQL query and returns a single scalar value
func (p *PrometheusClient) queryScalar(ctx context.Context, query string) (float64, error) {
	result, warnings, err := p.API.Query(ctx, query, time.Now())
	if err != nil {
		return 0, err
	}

	if len(warnings) > 0 {
		// Log warnings but don't fail
		// In production, use proper logging
		// log.Warn("Prometheus query warnings", "warnings", warnings)
	}

	switch v := result.(type) {
	case model.Vector:
		if len(v) == 0 {
			return 0, fmt.Errorf("no data returned from query")
		}
		return float64(v[0].Value), nil
	case *model.Scalar:
		return float64(v.Value), nil
	default:
		return 0, fmt.Errorf("unexpected result type: %T", result)
	}
}

// QueryRange queries Prometheus for time-series data over a range
func (p *PrometheusClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (model.Value, error) {
	r := promv1.Range{
		Start: start,
		End:   end,
		Step:  step,
	}

	result, warnings, err := p.API.QueryRange(ctx, query, r)
	if err != nil {
		return nil, err
	}

	if len(warnings) > 0 {
		// Log warnings
		// log.Warn("Prometheus range query warnings", "warnings", warnings)
	}

	return result, nil
}
