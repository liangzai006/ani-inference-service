package inference

import (
	"context"
	"errors"
	"testing"
)

func TestModelErrorClassIsBounded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "none", want: "none"},
		{name: "context", err: context.Canceled, want: "context"},
		{name: "provider missing", err: ErrOperationProviderMissing, want: "provider_missing"},
		{name: "not ready", err: ErrOperationNotReady, want: "not_ready"},
		{name: "provider", err: errors.New("model registry unavailable"), want: "provider_or_domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelErrorClass(tt.err); got != tt.want {
				t.Fatalf("modelErrorClass(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
