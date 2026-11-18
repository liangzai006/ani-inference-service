package gatewayauthz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type modelsStub struct{ result ModelsResult }

func (s modelsStub) ListModels(_ context.Context, key string) (ModelsResult, error) {
	if key != "test-key" {
		return ModelsResult{DenialCode: "invalid_key"}, nil
	}
	return s.result, nil
}

func TestModelsWithoutModelHeaderSupportsBothCredentials(t *testing.T) {
	handler := &ModelsHandler{Lister: modelsStub{ModelsResult{Allowed: true, Models: []Model{{ID: "allowed-model", Object: "model", OwnedBy: "ani"}}}}}
	for _, header := range []string{HeaderAuthorization, HeaderAPIKey} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			value := "test-key"
			if header == HeaderAuthorization {
				value = "Bearer " + value
			}
			req.Header.Set(header, value)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			var body struct {
				Object string  `json:"object"`
				Data   []Model `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 || body.Object != "list" || len(body.Data) != 1 || body.Data[0].ID != "allowed-model" {
				t.Fatalf("status=%d body=%s error=%v", response.Code, response.Body, err)
			}
		})
	}
}

func TestModelsRejectsInvalidCredentialsAndRetainsRateLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		key    string
		result ModelsResult
		status int
	}{
		{"missing", "", ModelsResult{}, 401},
		{"invalid", "wrong-key", ModelsResult{}, 401},
		{"limited", "test-key", ModelsResult{DenialCode: "rate_limited", RetryAfterSeconds: 12}, 429},
		{"empty", "test-key", ModelsResult{Allowed: true}, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if test.key != "" {
				req.Header.Set(HeaderAuthorization, "Bearer "+test.key)
			}
			response := httptest.NewRecorder()
			(&ModelsHandler{Lister: modelsStub{test.result}}).ServeHTTP(response, req)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d", response.Code, test.status)
			}
			if test.status == 429 && response.Header().Get(HeaderRetryAfter) != "12" {
				t.Fatal("missing rate-limit retry")
			}
			if test.name == "empty" && response.Body.String() != "{\"object\":\"list\",\"data\":[]}\n" {
				t.Fatalf("empty listing is not a JSON array: %s", response.Body)
			}
		})
	}
}

func TestOnlyExactForwardedModelListingDelegatesToAKAwareHandler(t *testing.T) {
	for _, test := range []struct {
		method, uri string
		status      int
	}{
		{"GET", "/v1/models", 200},
		{"GET", "/v1/models?client=dsh", 200},
		{"POST", "/v1/models", 401},
		{"GET", "/v1/models/other", 401},
		{"POST", "/v1/chat/completions", 401},
		{"GET", "/v1/%6dodels", 401},
	} {
		req := httptest.NewRequest(http.MethodPost, "/authz", nil)
		req.Header.Set(HeaderAuthzSource, "higress")
		req.Header.Set("x-forwarded-method", test.method)
		req.Header.Set("x-forwarded-uri", test.uri)
		response := httptest.NewRecorder()
		NewHandler(checkerFunc(func(context.Context, CheckRequest) (CheckResult, error) {
			t.Fatal("unexpected model call")
			return CheckResult{}, nil
		})).ServeHTTP(response, req)
		if response.Code != test.status {
			t.Fatalf("%s %s: status=%d want=%d", test.method, test.uri, response.Code, test.status)
		}
	}
}
