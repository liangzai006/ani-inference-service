package service

import (
	"context"
	"fmt"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// HealthChecker evaluates the health of an inference service
type HealthChecker struct {
	// In a real implementation, this would have:
	// - Kubernetes client to check pod readiness
	// - Metrics client to check error rates
	// - Endpoint client to test actual invocation
}

// CheckServiceHealth evaluates comprehensive health of an inference service
func (h *HealthChecker) CheckServiceHealth(ctx context.Context, svc *inferencev1.InferenceService) (*inferencev1.GetServiceHealthResponse, error) {
	now := timestamppb.Now()
	checks := make([]*inferencev1.HealthCheck, 0, 4)

	// Check 1: Runtime phase (deployment readiness)
	runtimeCheck := h.checkRuntimePhase(svc, now)
	checks = append(checks, runtimeCheck)

	// Check 2: Publication phase (endpoint availability)
	publicationCheck := h.checkPublicationPhase(svc, now)
	checks = append(checks, publicationCheck)

	// Check 3: Invocation health (actual request success)
	invocationCheck := h.checkInvocationHealth(svc, now)
	checks = append(checks, invocationCheck)

	// Check 4: Observation staleness
	observationCheck := h.checkObservationStaleness(svc, now)
	checks = append(checks, observationCheck)

	// Aggregate overall status
	overallStatus, reason := h.aggregateHealthStatus(checks)

	// Determine last success time
	var lastSuccess *timestamppb.Timestamp
	if svc.InvocationHealth == "healthy" && svc.ObservedAt != nil {
		lastSuccess = svc.ObservedAt
	}

	return &inferencev1.GetServiceHealthResponse{
		Status:          overallStatus,
		Reason:          reason,
		LastCheckTime:   now,
		LastSuccessTime: lastSuccess,
		Checks:          checks,
	}, nil
}

// checkRuntimePhase validates deployment readiness
func (h *HealthChecker) checkRuntimePhase(svc *inferencev1.InferenceService, now *timestamppb.Timestamp) *inferencev1.HealthCheck {
	check := &inferencev1.HealthCheck{
		Name:      "runtime",
		CheckedAt: now,
	}

	switch svc.RuntimePhase {
	case "ready":
		check.Status = "healthy"
		check.Message = "Runtime is ready"
	case "progressing", "pending":
		check.Status = "degraded"
		check.Message = fmt.Sprintf("Runtime is %s", svc.RuntimePhase)
	case "failed", "error":
		check.Status = "unhealthy"
		check.Message = fmt.Sprintf("Runtime has %s", svc.RuntimePhase)
	default:
		check.Status = "unknown"
		check.Message = fmt.Sprintf("Unknown runtime phase: %s", svc.RuntimePhase)
	}

	return check
}

// checkPublicationPhase validates endpoint publication status
func (h *HealthChecker) checkPublicationPhase(svc *inferencev1.InferenceService, now *timestamppb.Timestamp) *inferencev1.HealthCheck {
	check := &inferencev1.HealthCheck{
		Name:      "publication",
		CheckedAt: now,
	}

	switch svc.PublicationPhase {
	case "published":
		check.Status = "healthy"
		check.Message = "Service endpoint is published"
	case "publishing":
		check.Status = "degraded"
		check.Message = "Service endpoint is being published"
	case "unpublished", "withdrawn":
		check.Status = "unhealthy"
		check.Message = fmt.Sprintf("Service endpoint is %s", svc.PublicationPhase)
	case "failed":
		check.Status = "unhealthy"
		check.Message = "Publication failed"
	default:
		check.Status = "unknown"
		check.Message = fmt.Sprintf("Unknown publication phase: %s", svc.PublicationPhase)
	}

	return check
}

// checkInvocationHealth validates actual request success rate
func (h *HealthChecker) checkInvocationHealth(svc *inferencev1.InferenceService, now *timestamppb.Timestamp) *inferencev1.HealthCheck {
	check := &inferencev1.HealthCheck{
		Name:      "invocation",
		CheckedAt: now,
	}

	switch svc.InvocationHealth {
	case "healthy":
		check.Status = "healthy"
		check.Message = "Invocations are succeeding"
	case "unhealthy":
		check.Status = "unhealthy"
		check.Message = "Invocations are failing"
	case "degraded":
		check.Status = "degraded"
		check.Message = "Invocations are partially failing"
	case "unknown", "":
		check.Status = "unknown"
		check.Message = "Invocation health not yet determined"
	default:
		check.Status = "unknown"
		check.Message = fmt.Sprintf("Unknown invocation health: %s", svc.InvocationHealth)
	}

	return check
}

// checkObservationStaleness checks if the observation data is stale
func (h *HealthChecker) checkObservationStaleness(svc *inferencev1.InferenceService, now *timestamppb.Timestamp) *inferencev1.HealthCheck {
	check := &inferencev1.HealthCheck{
		Name:      "observation",
		CheckedAt: now,
	}

	if svc.ObservedAt == nil {
		check.Status = "unknown"
		check.Message = "No observation time recorded"
		return check
	}

	observedTime := svc.ObservedAt.AsTime()
	age := time.Since(observedTime)

	if age < 5*time.Minute {
		check.Status = "healthy"
		check.Message = fmt.Sprintf("Observation is fresh (%.0fs old)", age.Seconds())
	} else if age < 15*time.Minute {
		check.Status = "degraded"
		check.Message = fmt.Sprintf("Observation is aging (%.0fm old)", age.Minutes())
	} else {
		check.Status = "unhealthy"
		check.Message = fmt.Sprintf("Observation is stale (%.0fm old)", age.Minutes())
	}

	return check
}

// aggregateHealthStatus combines individual check results into overall status
func (h *HealthChecker) aggregateHealthStatus(checks []*inferencev1.HealthCheck) (string, string) {
	hasUnhealthy := false
	hasDegraded := false
	hasUnknown := false
	unhealthyReasons := make([]string, 0)
	degradedReasons := make([]string, 0)

	for _, check := range checks {
		switch check.Status {
		case "unhealthy":
			hasUnhealthy = true
			unhealthyReasons = append(unhealthyReasons, fmt.Sprintf("%s: %s", check.Name, check.Message))
		case "degraded":
			hasDegraded = true
			degradedReasons = append(degradedReasons, fmt.Sprintf("%s: %s", check.Name, check.Message))
		case "unknown":
			hasUnknown = true
		}
	}

	if hasUnhealthy {
		reason := "Service is unhealthy"
		if len(unhealthyReasons) > 0 {
			reason = fmt.Sprintf("%s: %v", reason, unhealthyReasons)
		}
		return "unhealthy", reason
	}

	if hasDegraded {
		reason := "Service is degraded"
		if len(degradedReasons) > 0 {
			reason = fmt.Sprintf("%s: %v", reason, degradedReasons)
		}
		return "degraded", reason
	}

	if hasUnknown {
		return "unknown", "Insufficient data to determine health status"
	}

	return "healthy", "All checks passed"
}
