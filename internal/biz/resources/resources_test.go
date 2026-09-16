package resources

import "testing"

func TestNormalizeAllowsLimitsOnlyExtendedResource(t *testing.T) {
	got, err := Normalize(Spec{Limits: map[string]string{"nvidia.com/gpu": "1"}})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Requests["nvidia.com/gpu"] != "1" {
		t.Fatalf("effective GPU request = %q, want 1", got.Requests["nvidia.com/gpu"])
	}
}

func TestNormalizeRejectsMismatchedExtendedResource(t *testing.T) {
	_, err := Normalize(Spec{
		Requests: map[string]string{"nvidia.com/gpu": "1"},
		Limits:   map[string]string{"nvidia.com/gpu": "2"},
	})
	if err == nil {
		t.Fatal("Normalize() error = nil, want mismatch error")
	}
}

func TestNormalizeRejectsRequestAboveLimit(t *testing.T) {
	_, err := Normalize(Spec{
		Requests: map[string]string{"cpu": "2"},
		Limits:   map[string]string{"cpu": "1"},
	})
	if err == nil {
		t.Fatal("Normalize() error = nil, want request/limit error")
	}
}

func TestNormalizeKeepsRegularRequestAndLimitIndependent(t *testing.T) {
	got, err := Normalize(Spec{Requests: map[string]string{"cpu": "2"}})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if _, ok := got.Limits["cpu"]; ok {
		t.Fatal("request-only CPU unexpectedly gained a limit")
	}
}
