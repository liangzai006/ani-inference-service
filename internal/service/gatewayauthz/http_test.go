package gatewayauthz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type checkerFunc func(context.Context, CheckRequest) (CheckResult, error)

func (f checkerFunc) Check(ctx context.Context, req CheckRequest) (CheckResult, error) {
	return f(ctx, req)
}

func TestHandlerAllowsAndOverwritesIdentityHeaders(t *testing.T) {
	var got CheckRequest
	h := NewHandler(checkerFunc(func(_ context.Context, req CheckRequest) (CheckResult, error) {
		got = req
		return CheckResult{Allowed: true, TenantID: "tenant-a", InferenceKeyID: "key-a"}, nil
	}))
	req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
	req.Header.Add(HeaderAPIKey, "iak_secret")
	req.Header.Add(HeaderModel, "model-a")
	req.Header.Add("x-ani-inference-service-id", "client-supplied")
	req.Header.Add(HeaderAuthzSource, "higress")
	req.Header.Add(HeaderTenantID, "attacker-tenant")
	req.Header.Add(HeaderInferenceKey, "attacker-key")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got.APIKey != "iak_secret" || got.ServiceID != "" || got.ServedModelName != "model-a" || got.RequestCost != 1 || got.RequestID == "" {
		t.Fatalf("checker request = %+v", got)
	}
	if response.Header().Get(HeaderTenantID) != "tenant-a" || response.Header().Get(HeaderInferenceKey) != "key-a" {
		t.Fatalf("identity headers = %v", response.Header())
	}
}

func TestHandlerRejectsMissingOrDuplicateSecret(t *testing.T) {
	h := NewHandler(checkerFunc(func(context.Context, CheckRequest) (CheckResult, error) {
		t.Fatal("checker should not run")
		return CheckResult{}, nil
	}))
	for name, setup := range map[string]func(*http.Request){
		"missing":   func(*http.Request) {},
		"duplicate": func(r *http.Request) { r.Header.Add(HeaderAPIKey, "one"); r.Header.Add(HeaderAPIKey, "two") },
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
			req.Header.Set(HeaderModel, "model-a")
			req.Header.Set(HeaderAuthzSource, "higress")
			setup(req)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
		})
	}
}

func TestHandlerMapsScopeAndRateDenials(t *testing.T) {
	for code, want := range map[string]int{"scope_denied": http.StatusForbidden, "rate_limited": http.StatusTooManyRequests, "revoked": http.StatusUnauthorized} {
		t.Run(code, func(t *testing.T) {
			h := NewHandler(checkerFunc(func(context.Context, CheckRequest) (CheckResult, error) {
				return CheckResult{DenialCode: code, RetryAfterSeconds: 3}, nil
			}))
			req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
			req.Header.Set(HeaderAPIKey, "iak_secret")
			req.Header.Set(HeaderModel, "model-a")
			req.Header.Set(HeaderAuthzSource, "higress")
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != want {
				t.Fatalf("status = %d, want %d", response.Code, want)
			}
			if code == "rate_limited" && response.Header().Get(HeaderRetryAfter) != "3" {
				t.Fatalf("Retry-After = %q", response.Header().Get(HeaderRetryAfter))
			}
		})
	}
}

func TestHandlerFailsClosedOnCheckerErrorOrIdentityMissing(t *testing.T) {
	h := NewHandler(checkerFunc(func(context.Context, CheckRequest) (CheckResult, error) {
		return CheckResult{}, errors.New("database unavailable")
	}))
	for _, missing := range []string{"source", "model"} {
		t.Run(missing, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
			req.Header.Set(HeaderAPIKey, "iak_secret")
			if missing == "source" {
				req.Header.Set(HeaderModel, "model-a")
			} else {
				req.Header.Set(HeaderAuthzSource, "higress")
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			want := http.StatusForbidden
			if missing == "source" {
				want = http.StatusForbidden
			}
			if response.Code != want {
				t.Fatalf("status = %d, want %d", response.Code, want)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
	req.Header.Set(HeaderAPIKey, "iak_secret")
	req.Header.Set(HeaderModel, "model-a")
	req.Header.Set(HeaderAuthzSource, "higress")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("checker error status = %d, want 503", response.Code)
	}
}
