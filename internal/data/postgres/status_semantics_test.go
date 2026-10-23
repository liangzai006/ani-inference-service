package postgres

import "testing"

func TestNormalizeStatusSemantics(t *testing.T) {
	if got := normalizeRuntimePhase("unknown"); got != "pending" {
		t.Fatalf("runtime unknown = %q, want pending", got)
	}
	if got := normalizePublicationPhase("unknown"); got != "withdrawn" {
		t.Fatalf("publication unknown = %q, want withdrawn", got)
	}
	if got := normalizeInvocationHealth("pending", "withdrawn", "unknown"); got != "unhealthy" {
		t.Fatalf("pre-ready health = %q, want unhealthy", got)
	}
	if got := normalizeInvocationHealth("ready", "published", "unknown"); got != "unknown" {
		t.Fatalf("inconclusive ready health = %q, want unknown", got)
	}
	if got := normalizeInvocationHealth("ready", "published", "healthy"); got != "healthy" {
		t.Fatalf("healthy health = %q, want healthy", got)
	}
}
