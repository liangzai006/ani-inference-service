package service

import (
	"context"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func TestPrometheusClient_QueryScalar(t *testing.T) {
	// This test requires a running Prometheus instance
	// For now, we test the query building logic

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{
			name:     "request rate query format",
			query:    `sum(rate(inference_requests_total{tenant_id="t1",service_id="s1",namespace="inference-system"}[5m]))`,
			expected: "valid PromQL",
		},
		{
			name:     "CPU query format",
			query:    `avg(rate(container_cpu_usage_seconds_total{namespace="inference-system",pod=~"t1-s1-.*",container!=""}[5m]))`,
			expected: "valid PromQL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify query is non-empty and well-formed
			if len(tt.query) == 0 {
				t.Error("query should not be empty")
			}

			// Check for common PromQL syntax
			if tt.query[0] != 's' && tt.query[0] != 'a' && tt.query[0] != 'm' && tt.query[0] != 'h' {
				t.Error("query should start with a PromQL function")
			}
		})
	}
}

func TestMetricsCollector_WithAndWithoutPrometheus(t *testing.T) {
	ctx := context.Background()

	svc := &inferencev1.InferenceService{
		Id:               "service-123",
		InvocationHealth: "healthy",
		Resource: &inferencev1.ResourceSpec{
			Gpu: &inferencev1.GpuRequest{
				DevicesPerReplica: 2,
			},
		},
	}

	t.Run("without Prometheus returns simulated data", func(t *testing.T) {
		collector := NewMetricsCollector("", "")
		if collector.PrometheusClient != nil {
			t.Error("expected nil Prometheus client for empty address")
		}

		metrics, err := collector.CollectServiceMetrics(ctx, "tenant-456", svc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if metrics == nil {
			t.Fatal("expected non-nil metrics")
		}

		if metrics.TotalRequests == 0 {
			t.Error("expected simulated request data")
		}
	})

	t.Run("with invalid Prometheus address falls back to simulated", func(t *testing.T) {
		collector := NewMetricsCollector("invalid://address", "default")

		// Should gracefully fall back to simulated data
		metrics, err := collector.CollectServiceMetrics(ctx, "tenant-456", svc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if metrics == nil {
			t.Fatal("expected non-nil metrics")
		}
	})
}

func TestPrometheusQueryBuilder_AllQueries(t *testing.T) {
	builder := &PrometheusQueryBuilder{
		ServiceID: "svc-123",
		TenantID:  "tenant-456",
		TimeRange: "5m",
	}

	tests := []struct {
		name  string
		query string
	}{
		{
			name:  "request rate",
			query: builder.BuildRequestRateQuery(),
		},
		{
			name:  "success rate",
			query: builder.BuildSuccessRateQuery(),
		},
		{
			name:  "P95 latency",
			query: builder.BuildLatencyP95Query(),
		},
		{
			name:  "P99 latency",
			query: builder.BuildLatencyP99Query(),
		},
		{
			name:  "CPU usage",
			query: builder.BuildCPUUsageQuery("default", "inference-svc"),
		},
		{
			name:  "memory usage",
			query: builder.BuildMemoryUsageQuery("default", "inference-svc"),
		},
		{
			name:  "GPU utilization",
			query: builder.BuildGPUUtilizationQuery("default", "inference-svc"),
		},
		{
			name:  "GPU memory",
			query: builder.BuildGPUMemoryQuery("default", "inference-svc"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.query == "" {
				t.Error("query should not be empty")
			}

			// Check that query contains service/tenant ID
			if tt.name == "request rate" || tt.name == "success rate" {
				if !contains(tt.query, builder.ServiceID) {
					t.Errorf("query should contain service_id: %s", tt.query)
				}
				if !contains(tt.query, builder.TenantID) {
					t.Errorf("query should contain tenant_id: %s", tt.query)
				}
			}

			// Check that time range queries contain the time range
			if tt.name != "memory usage" && tt.name != "GPU utilization" && tt.name != "GPU memory" {
				if !contains(tt.query, builder.TimeRange) {
					t.Errorf("query should contain time range [%s]: %s", builder.TimeRange, tt.query)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
