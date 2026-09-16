package work

import (
	"context"
	"errors"
	"testing"
)

func TestErrorClassUsesBoundedValues(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "none", want: "none"},
		{name: "stale", err: ErrStaleGeneration, want: "stale_generation"},
		{name: "lease", err: ErrLeaseLost, want: "lease_lost"},
		{name: "context", err: context.Canceled, want: "context"},
		{name: "dependency", err: errors.New("database unavailable"), want: "dependency_or_domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorClass(tt.err); got != tt.want {
				t.Fatalf("errorClass(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
