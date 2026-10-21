package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InvocationTester sends test requests to inference endpoints
type InvocationTester struct {
	HTTPClient *http.Client
}

// NewInvocationTester creates a new test invocation client
func NewInvocationTester() *InvocationTester {
	return &InvocationTester{
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			// In production, configure TLS, retries, etc.
		},
	}
}

// TestServiceInvocation sends a test request to an inference service
func (t *InvocationTester) TestServiceInvocation(
	ctx context.Context,
	svc *inferencev1.InferenceService,
	req *inferencev1.TestInvocationRequest,
) (*inferencev1.TestInvocationResponse, error) {
	// Determine endpoint URL
	endpointURL := req.GetEndpointUrl()
	if endpointURL == "" {
		// In production, construct from service's published endpoint
		// For now, return error if not provided
		return nil, status.Error(codes.InvalidArgument, "endpoint_url is required (service endpoint discovery not yet implemented)")
	}

	// Prepare request body
	requestBody := req.GetRequestBody()
	if requestBody == "" {
		// If no body provided, use default OpenAI-compatible format
		modelName := svc.GetServedModelName()
		if modelName == "" {
			modelName = "default-model"
		}
		requestBody = BuildDefaultOpenAIRequest(modelName)
	}

	// Validate request body is valid JSON
	if !json.Valid([]byte(requestBody)) {
		return nil, status.Error(codes.InvalidArgument, "request_body must be valid JSON")
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpointURL, bytes.NewBufferString(requestBody))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create HTTP request: %v", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "ani-inference-service/test-invocation")

	// Add custom headers from request
	for key, value := range req.GetHeaders() {
		httpReq.Header.Set(key, value)
	}

	// Add authentication headers if specified
	if err := t.addAuthHeaders(httpReq, req); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid authentication: %v", err)
	}

	// Measure latency
	startTime := time.Now()

	// Send request
	httpResp, err := t.HTTPClient.Do(httpReq)
	if err != nil {
		return &inferencev1.TestInvocationResponse{
			StatusCode:   0,
			ErrorMessage: fmt.Sprintf("HTTP request failed: %v", err),
			LatencyMs:    time.Since(startTime).Milliseconds(),
		}, nil // Return response with error, not gRPC error
	}
	defer httpResp.Body.Close()

	latencyMs := time.Since(startTime).Milliseconds()

	// Read response body
	responseBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return &inferencev1.TestInvocationResponse{
			StatusCode:   int32(httpResp.StatusCode),
			ErrorMessage: fmt.Sprintf("failed to read response body: %v", err),
			LatencyMs:    latencyMs,
		}, nil
	}

	// Collect response headers
	responseHeaders := make(map[string]string)
	for key, values := range httpResp.Header {
		if len(values) > 0 {
			responseHeaders[key] = values[0]
		}
	}

	// Build response
	response := &inferencev1.TestInvocationResponse{
		StatusCode:      int32(httpResp.StatusCode),
		ResponseBody:    string(responseBody),
		ResponseHeaders: responseHeaders,
		LatencyMs:       latencyMs,
	}

	// Set error message if status indicates failure
	if httpResp.StatusCode >= 400 {
		response.ErrorMessage = fmt.Sprintf("HTTP %d: %s", httpResp.StatusCode, httpResp.Status)
	}

	return response, nil
}

// addAuthHeaders adds authentication headers based on the request configuration
func (t *InvocationTester) addAuthHeaders(httpReq *http.Request, req *inferencev1.TestInvocationRequest) error {
	authMethod := req.GetAuthMethod()
	if authMethod == "" || authMethod == "none" {
		return nil
	}

	switch authMethod {
	case "bearer":
		// Look for Authorization header or Bearer token
		if auth := req.GetHeaders()["Authorization"]; auth != "" {
			// Already set, use it
			return nil
		}
		if auth := req.GetHeaders()["authorization"]; auth != "" {
			httpReq.Header.Set("Authorization", auth)
			return nil
		}
		return fmt.Errorf("bearer auth requires Authorization header")

	case "api_key":
		// Look for API key header (common variations)
		if apiKey := req.GetHeaders()["X-API-Key"]; apiKey != "" {
			return nil
		}
		if apiKey := req.GetHeaders()["x-api-key"]; apiKey != "" {
			httpReq.Header.Set("X-API-Key", apiKey)
			return nil
		}
		return fmt.Errorf("api_key auth requires X-API-Key header")

	default:
		return fmt.Errorf("unsupported auth method: %s", authMethod)
	}
}

// ValidateOpenAIFormat validates that a request body conforms to OpenAI API format
func ValidateOpenAIFormat(requestBody string) error {
	var body map[string]interface{}
	if err := json.Unmarshal([]byte(requestBody), &body); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	// Check for required fields
	if _, ok := body["model"]; !ok {
		return fmt.Errorf("missing required field: model")
	}

	// Must have either 'prompt' (completion) or 'messages' (chat)
	hasPrompt := body["prompt"] != nil
	hasMessages := body["messages"] != nil

	if !hasPrompt && !hasMessages {
		return fmt.Errorf("missing required field: prompt or messages")
	}

	return nil
}

// BuildDefaultOpenAIRequest builds a default OpenAI-compatible test request
func BuildDefaultOpenAIRequest(modelName string) string {
	request := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello, this is a test message from ani-inference-service",
			},
		},
		"max_tokens":  50,
		"temperature": 0.7,
	}

	body, _ := json.Marshal(request)
	return string(body)
}
