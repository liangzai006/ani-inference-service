package service

import (
	"context"
	"strings"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func TestMetricsCollector_CollectServiceMetrics(t *testing.T) {
	collector := NewMetricsCollector("", "") // Use simulated data
	ctx := context.Background()

	tests := []struct {
		name             string
		invocationHealth string
		gpuDevices       uint32
		expectRequests   bool
		expectGPU        bool
	}{
		{
			name:             "healthy service has metrics",
			invocationHealth: "healthy",
			gpuDevices:       0,
			expectRequests:   true,
			expectGPU:        false,
		},
		{
			name:             "unhealthy service has reduced metrics",
			invocationHealth: "unhealthy",
			gpuDevices:       0,
			expectRequests:   true,
			expectGPU:        false,
		},
		{
			name:             "GPU service includes GPU metrics",
			invocationHealth: "healthy",
			gpuDevices:       2,
			expectRequests:   true,
			expectGPU:        true,
		},
		{
			name:             "unknown health has no request metrics",
			invocationHealth: "unknown",
			gpuDevices:       0,
			expectRequests:   false,
			expectGPU:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inferencev1.InferenceService{
				InvocationHealth: tt.invocationHealth,
				Resource:         &inferencev1.ResourceSpec{},
			}

			if tt.gpuDevices > 0 {
				svc.Resource.Gpu = &inferencev1.GpuRequest{
					DevicesPerReplica: tt.gpuDevices,
					Replicas:          1,
				}
			}

			metrics, err := collector.CollectServiceMetrics(ctx, "tenant-123", svc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if metrics == nil {
				t.Fatal("expected non-nil metrics response")
			}

			if tt.expectRequests && metrics.TotalRequests == 0 {
				t.Error("expected request metrics for healthy/unhealthy service")
			}

			if tt.expectGPU {
				if metrics.ResourceUsage == nil || metrics.ResourceUsage.Gpu == nil {
					t.Error("expected GPU metrics for GPU service")
				}
			}

			if !tt.expectGPU && metrics.ResourceUsage != nil && metrics.ResourceUsage.Gpu != nil {
				t.Error("unexpected GPU metrics for CPU-only service")
			}
		})
	}
}

func TestPrometheusQueryBuilder_BuildQueries(t *testing.T) {
	builder := &PrometheusQueryBuilder{
		ServiceID: "svc-123",
		TenantID:  "tenant-456",
		TimeRange: "5m",
	}

	tests := []struct {
		name      string
		queryFunc func() string
		contains  []string
	}{
		{
			name:      "request rate query",
			queryFunc: builder.BuildRequestRateQuery,
			contains:  []string{"inference_requests_total", "svc-123", "tenant-456", "[5m]"},
		},
		{
			name:      "success rate query",
			queryFunc: builder.BuildSuccessRateQuery,
			contains:  []string{"inference_requests_total", "status=\"success\"", "* 100"},
		},
		{
			name:      "P95 latency query",
			queryFunc: builder.BuildLatencyP95Query,
			contains:  []string{"histogram_quantile", "0.95", "inference_request_duration_seconds_bucket"},
		},
		{
			name:      "P99 latency query",
			queryFunc: builder.BuildLatencyP99Query,
			contains:  []string{"histogram_quantile", "0.99", "inference_request_duration_seconds_bucket"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := tt.queryFunc()

			for _, substr := range tt.contains {
				if !strings.Contains(query, substr) {
					t.Errorf("expected query to contain %q, got: %s", substr, query)
				}
			}
		})
	}
}

func TestPrometheusQueryBuilder_BuildResourceQueries(t *testing.T) {
	builder := &PrometheusQueryBuilder{
		TimeRange: "5m",
	}
	namespace := "inference-prod"
	serviceName := "llama-7b"

	tests := []struct {
		name      string
		queryFunc func(string, string) string
		contains  []string
	}{
		{
			name:      "CPU usage query",
			queryFunc: builder.BuildCPUUsageQuery,
			contains:  []string{"container_cpu_usage_seconds_total", "inference-prod", "llama-7b"},
		},
		{
			name:      "memory usage query",
			queryFunc: builder.BuildMemoryUsageQuery,
			contains:  []string{"container_memory_working_set_bytes", "inference-prod", "llama-7b"},
		},
		{
			name:      "GPU utilization query",
			queryFunc: builder.BuildGPUUtilizationQuery,
			contains:  []string{"nvidia_gpu_duty_cycle", "inference-prod", "llama-7b"},
		},
		{
			name:      "GPU memory query",
			queryFunc: builder.BuildGPUMemoryQuery,
			contains:  []string{"nvidia_gpu_memory_used_bytes", "inference-prod", "llama-7b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := tt.queryFunc(namespace, serviceName)

			for _, substr := range tt.contains {
				if !strings.Contains(query, substr) {
					t.Errorf("expected query to contain %q, got: %s", substr, query)
				}
			}
		})
	}
}
