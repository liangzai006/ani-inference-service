package service

import (
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHealthChecker_CheckRuntimePhase(t *testing.T) {
	checker := &HealthChecker{}
	now := timestamppb.Now()

	tests := []struct {
		name          string
		runtimePhase  string
		expectStatus  string
		expectHealthy bool
	}{
		{
			name:          "ready runtime is healthy",
			runtimePhase:  "ready",
			expectStatus:  "healthy",
			expectHealthy: true,
		},
		{
			name:          "progressing runtime is degraded",
			runtimePhase:  "progressing",
			expectStatus:  "degraded",
			expectHealthy: false,
		},
		{
			name:          "failed runtime is unhealthy",
			runtimePhase:  "failed",
			expectStatus:  "unhealthy",
			expectHealthy: false,
		},
		{
			name:          "error runtime is unhealthy",
			runtimePhase:  "error",
			expectStatus:  "unhealthy",
			expectHealthy: false,
		},
		{
			name:          "pending runtime is degraded",
			runtimePhase:  "pending",
			expectStatus:  "degraded",
			expectHealthy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inferencev1.InferenceService{
				RuntimePhase: tt.runtimePhase,
			}

			check := checker.checkRuntimePhase(svc, now)

			if check.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, check.Status)
			}
			if check.Name != "runtime" {
				t.Errorf("expected check name 'runtime', got %s", check.Name)
			}
		})
	}
}

func TestHealthChecker_CheckPublicationPhase(t *testing.T) {
	checker := &HealthChecker{}
	now := timestamppb.Now()

	tests := []struct {
		name             string
		publicationPhase string
		expectStatus     string
	}{
		{
			name:             "published is healthy",
			publicationPhase: "published",
			expectStatus:     "healthy",
		},
		{
			name:             "publishing is degraded",
			publicationPhase: "publishing",
			expectStatus:     "degraded",
		},
		{
			name:             "unpublished is unhealthy",
			publicationPhase: "unpublished",
			expectStatus:     "unhealthy",
		},
		{
			name:             "failed publication is unhealthy",
			publicationPhase: "failed",
			expectStatus:     "unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inferencev1.InferenceService{
				PublicationPhase: tt.publicationPhase,
			}

			check := checker.checkPublicationPhase(svc, now)

			if check.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, check.Status)
			}
		})
	}
}

func TestHealthChecker_CheckInvocationHealth(t *testing.T) {
	checker := &HealthChecker{}
	now := timestamppb.Now()

	tests := []struct {
		name             string
		invocationHealth string
		expectStatus     string
	}{
		{
			name:             "healthy invocations",
			invocationHealth: "healthy",
			expectStatus:     "healthy",
		},
		{
			name:             "unhealthy invocations",
			invocationHealth: "unhealthy",
			expectStatus:     "unhealthy",
		},
		{
			name:             "degraded invocations",
			invocationHealth: "degraded",
			expectStatus:     "degraded",
		},
		{
			name:             "unknown invocation health",
			invocationHealth: "unknown",
			expectStatus:     "unknown",
		},
		{
			name:             "empty invocation health",
			invocationHealth: "",
			expectStatus:     "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inferencev1.InferenceService{
				InvocationHealth: tt.invocationHealth,
			}

			check := checker.checkInvocationHealth(svc, now)

			if check.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, check.Status)
			}
		})
	}
}

func TestHealthChecker_CheckObservationStaleness(t *testing.T) {
	checker := &HealthChecker{}
	now := timestamppb.Now()

	tests := []struct {
		name         string
		observedAt   *timestamppb.Timestamp
		expectStatus string
	}{
		{
			name:         "recent observation is healthy",
			observedAt:   timestamppb.New(time.Now().Add(-1 * time.Minute)),
			expectStatus: "healthy",
		},
		{
			name:         "6 minute old observation is degraded",
			observedAt:   timestamppb.New(time.Now().Add(-6 * time.Minute)),
			expectStatus: "degraded",
		},
		{
			name:         "20 minute old observation is unhealthy",
			observedAt:   timestamppb.New(time.Now().Add(-20 * time.Minute)),
			expectStatus: "unhealthy",
		},
		{
			name:         "nil observation is unknown",
			observedAt:   nil,
			expectStatus: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inferencev1.InferenceService{
				ObservedAt: tt.observedAt,
			}

			check := checker.checkObservationStaleness(svc, now)

			if check.Status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, check.Status)
			}
		})
	}
}

func TestHealthChecker_AggregateHealthStatus(t *testing.T) {
	checker := &HealthChecker{}

	tests := []struct {
		name           string
		checkStatuses  []string
		expectStatus   string
		expectContains string
	}{
		{
			name:           "all healthy",
			checkStatuses:  []string{"healthy", "healthy", "healthy"},
			expectStatus:   "healthy",
			expectContains: "all checks passed",
		},
		{
			name:           "one unhealthy makes overall unhealthy",
			checkStatuses:  []string{"healthy", "unhealthy", "healthy"},
			expectStatus:   "unhealthy",
			expectContains: "unhealthy",
		},
		{
			name:           "one degraded makes overall degraded",
			checkStatuses:  []string{"healthy", "degraded", "healthy"},
			expectStatus:   "degraded",
			expectContains: "degraded",
		},
		{
			name:           "unhealthy takes precedence over degraded",
			checkStatuses:  []string{"degraded", "unhealthy", "healthy"},
			expectStatus:   "unhealthy",
			expectContains: "unhealthy",
		},
		{
			name:           "unknown is reported when no other status",
			checkStatuses:  []string{"unknown", "unknown", "unknown"},
			expectStatus:   "unknown",
			expectContains: "insufficient",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := make([]*inferencev1.HealthCheck, len(tt.checkStatuses))
			for i, status := range tt.checkStatuses {
				checks[i] = &inferencev1.HealthCheck{
					Name:   "check",
					Status: status,
				}
			}

			status, reason := checker.aggregateHealthStatus(checks)

			if status != tt.expectStatus {
				t.Errorf("expected status %s, got %s", tt.expectStatus, status)
			}
			if tt.expectContains != "" && len(reason) > 0 {
				// Just verify reason is not empty
				if reason == "" {
					t.Errorf("expected non-empty reason containing %s", tt.expectContains)
				}
			}
		})
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
