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

func TestHandlerBearerAndAPIKeyCredentials(t *testing.T) {
	tests := []struct {
		name   string
		apiKey []string
		auth   []string
		allow  bool
	}{
		{name: "api key", apiKey: []string{"iak_secret"}, allow: true},
		{name: "bearer", auth: []string{"Bearer iak_secret"}, allow: true},
		{name: "bearer case insensitive", auth: []string{"bearer iak_secret"}, allow: true},
		{name: "matching headers", apiKey: []string{"iak_secret"}, auth: []string{"Bearer iak_secret"}, allow: true},
		{name: "conflicting headers", apiKey: []string{"iak_other"}, auth: []string{"Bearer iak_secret"}},
		{name: "empty api key with bearer", apiKey: []string{""}, auth: []string{"Bearer iak_secret"}},
		{name: "duplicate api key", apiKey: []string{"iak_secret", "iak_secret"}},
		{name: "duplicate authorization", auth: []string{"Bearer iak_secret", "Bearer iak_secret"}},
		{name: "comma joined authorization", auth: []string{"Bearer iak_secret, Bearer iak_secret"}},
		{name: "wrong scheme", auth: []string{"Basic iak_secret"}},
		{name: "missing bearer token", auth: []string{"Bearer"}},
		{name: "extra bearer token", auth: []string{"Bearer iak_secret extra"}},
		{name: "empty authorization with api key", apiKey: []string{"iak_secret"}, auth: []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			h := NewHandler(checkerFunc(func(_ context.Context, req CheckRequest) (CheckResult, error) {
				calls++
				if req.APIKey != "iak_secret" || req.ServedModelName != "model-a" {
					t.Fatalf("unexpected credential or model passed to checker")
				}
				return CheckResult{Allowed: true, TenantID: "tenant-a", InferenceKeyID: "key-a"}, nil
			}))
			req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
			req.Header.Set(HeaderModel, "model-a")
			req.Header.Set(HeaderAuthzSource, "higress")
			for _, key := range tt.apiKey {
				req.Header.Add(HeaderAPIKey, key)
			}
			for _, auth := range tt.auth {
				req.Header.Add(HeaderAuthorization, auth)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			wantStatus, wantCalls := http.StatusUnauthorized, 0
			if tt.allow {
				wantStatus, wantCalls = http.StatusOK, 1
			}
			if response.Code != wantStatus || calls != wantCalls {
				t.Fatalf("status = %d, checker calls = %d; want %d, %d", response.Code, calls, wantStatus, wantCalls)
			}
		})
	}
}

func TestHandlerMapsScopeAndRateDenials(t *testing.T) {
	for code, want := range map[string]int{"scope_denied": http.StatusForbidden, "rate_limited": http.StatusTooManyRequests, "revoked": http.StatusUnauthorized} {
		for _, bearer := range []bool{false, true} {
			name := code + "/x-api-key"
			if bearer {
				name = code + "/bearer"
			}
			t.Run(name, func(t *testing.T) {
				h := NewHandler(checkerFunc(func(context.Context, CheckRequest) (CheckResult, error) {
					return CheckResult{DenialCode: code, RetryAfterSeconds: 3}, nil
				}))
				req := httptest.NewRequest(http.MethodPost, "http://authz/authz", nil)
				if bearer {
					req.Header.Set(HeaderAuthorization, "Bearer iak_secret")
				} else {
					req.Header.Set(HeaderAPIKey, "iak_secret")
				}
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
