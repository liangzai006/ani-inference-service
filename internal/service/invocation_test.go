package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/status"
)

func TestInvocationTester_TestServiceInvocation(t *testing.T) {
	// Create test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "test-123", "choices": [{"text": "Hello"}]}`))
	}))
	defer server.Close()

	tester := NewInvocationTester()
	ctx := context.Background()

	svc := &inferencev1.InferenceService{
		Id:              "svc-123",
		ServedModelName: "test-model",
	}

	tests := []struct {
		name           string
		request        *inferencev1.TestInvocationRequest
		expectStatus   int32
		expectError    bool
		expectResponse bool
	}{
		{
			name: "successful invocation with auth",
			request: &inferencev1.TestInvocationRequest{
				EndpointUrl: server.URL,
				RequestBody: `{"model": "test-model", "prompt": "test"}`,
				Headers: map[string]string{
					"Authorization": "Bearer test-token",
				},
			},
			expectStatus:   200,
			expectError:    false,
			expectResponse: true,
		},
		{
			name: "unauthorized invocation",
			request: &inferencev1.TestInvocationRequest{
				EndpointUrl: server.URL,
				RequestBody: `{"model": "test-model", "prompt": "test"}`,
			},
			expectStatus:   401,
			expectError:    false, // Not a gRPC error, but HTTP error
			expectResponse: true,
		},
		{
			name: "missing endpoint URL",
			request: &inferencev1.TestInvocationRequest{
				RequestBody: `{"model": "test-model", "prompt": "test"}`,
			},
			expectStatus: 0,
			expectError:  true, // This should return a gRPC error
		},
		{
			name: "invalid JSON body",
			request: &inferencev1.TestInvocationRequest{
				EndpointUrl: server.URL,
				RequestBody: `{invalid json`,
			},
			expectStatus: 0,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tester.TestServiceInvocation(ctx, svc, tt.request)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got none")
				}
				// Check it's a gRPC error
				if _, ok := status.FromError(err); !ok {
					t.Error("expected gRPC status error")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resp.StatusCode != tt.expectStatus {
				t.Errorf("expected status %d, got %d", tt.expectStatus, resp.StatusCode)
			}

			if tt.expectResponse && resp.ResponseBody == "" {
				t.Error("expected non-empty response body")
			}

			if resp.LatencyMs < 0 {
				t.Error("expected non-negative latency measurement")
			}
		})
	}
}

func TestValidateOpenAIFormat(t *testing.T) {
	tests := []struct {
		name        string
		requestBody string
		expectError bool
	}{
		{
			name:        "valid chat completion request",
			requestBody: `{"model": "gpt-4", "messages": [{"role": "user", "content": "test"}]}`,
			expectError: false,
		},
		{
			name:        "valid completion request",
			requestBody: `{"model": "gpt-4", "prompt": "test"}`,
			expectError: false,
		},
		{
			name:        "missing model field",
			requestBody: `{"messages": [{"role": "user", "content": "test"}]}`,
			expectError: true,
		},
		{
			name:        "missing prompt and messages",
			requestBody: `{"model": "gpt-4"}`,
			expectError: true,
		},
		{
			name:        "invalid JSON",
			requestBody: `{invalid`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOpenAIFormat(tt.requestBody)

			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBuildDefaultOpenAIRequest(t *testing.T) {
	modelName := "test-model"
	body := BuildDefaultOpenAIRequest(modelName)

	if body == "" {
		t.Fatal("expected non-empty request body")
	}

	// Validate it's valid OpenAI format
	if err := ValidateOpenAIFormat(body); err != nil {
		t.Errorf("default request is not valid OpenAI format: %v", err)
	}
}
