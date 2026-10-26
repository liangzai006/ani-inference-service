// Package gatewayauthz adapts Higress ext-auth requests to the Inference
// access-key check contract. It never persists or logs the presented secret.
package gatewayauthz

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	HeaderAPIKey       = "x-api-key"
	HeaderModel        = "x-higress-llm-model"
	HeaderRequestID    = "x-request-id"
	HeaderAuthzSource  = "x-ani-authz-source"
	HeaderTenantID     = "x-ani-tenant-id"
	HeaderInferenceKey = "x-ani-inference-key-id"
	HeaderRetryAfter   = "Retry-After"
)

var ErrInvalidRequest = errors.New("invalid gateway authorization request")

// CheckRequest is the transport-neutral request sent to Inference.
type CheckRequest struct {
	APIKey string
	// ServiceID is reserved for a future trusted route-context adapter. The
	// public Higress HTTP handler intentionally leaves it empty because a
	// route-level header may be applied after AUTHN and must not be trusted
	// when supplied by a client.
	ServiceID       string
	ServedModelName string
	RequestID       string
	RequestCost     int32
}

// CheckResult is the transport-neutral decision returned by Inference.
type CheckResult struct {
	Allowed           bool
	TenantID          string
	InferenceKeyID    string
	DenialCode        string
	RetryAfterSeconds int32
	PolicyVersion     int64
}

// Checker is implemented by the mTLS Inference client. Keeping this port in
// the inbound adapter package prevents the HTTP transport from depending on a
// concrete gRPC or PostgreSQL implementation.
type Checker interface {
	Check(context.Context, CheckRequest) (CheckResult, error)
}

// Handler serves the HTTP contract consumed by Higress ext-auth. Requests
// from clients must arrive through the configured ext-auth plugin marker;
// this is a routing guard, not an authentication credential.
type Handler struct {
	Checker        Checker
	RequiredSource string
}

func NewHandler(checker Checker) *Handler {
	return &Handler{Checker: checker, RequiredSource: "higress"}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r == nil {
		return
	}
	writeCommonHeaders(w)
	if r.Method != http.MethodPost || r.URL.Path != "/authz" {
		writeStatus(w, http.StatusNotFound)
		return
	}
	if h == nil || h.Checker == nil {
		writeStatus(w, http.StatusServiceUnavailable)
		return
	}
	if required := strings.TrimSpace(h.RequiredSource); required != "" && strings.TrimSpace(r.Header.Get(HeaderAuthzSource)) != required {
		writeStatus(w, http.StatusForbidden)
		return
	}
	apiKey, ok := singleHeader(r.Header, HeaderAPIKey)
	if !ok || strings.TrimSpace(apiKey) == "" {
		writeStatus(w, http.StatusUnauthorized)
		return
	}
	model, ok := singleHeader(r.Header, HeaderModel)
	if !ok || strings.TrimSpace(model) == "" {
		writeStatus(w, http.StatusForbidden)
		return
	}
	requestID := strings.TrimSpace(r.Header.Get(HeaderRequestID))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	result, err := h.Checker.Check(r.Context(), CheckRequest{
		APIKey:          apiKey,
		ServedModelName: strings.TrimSpace(model),
		RequestID:       requestID,
		RequestCost:     1,
	})
	if err != nil {
		// An unavailable authority must never become a gateway allow.
		writeStatus(w, http.StatusServiceUnavailable)
		return
	}
	if result.Allowed {
		if strings.TrimSpace(result.TenantID) == "" || strings.TrimSpace(result.InferenceKeyID) == "" {
			writeStatus(w, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set(HeaderTenantID, result.TenantID)
		w.Header().Set(HeaderInferenceKey, result.InferenceKeyID)
		writeStatus(w, http.StatusOK)
		return
	}
	statusCode := denialStatus(result.DenialCode)
	if statusCode == http.StatusTooManyRequests && result.RetryAfterSeconds > 0 {
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(int(result.RetryAfterSeconds)))
	}
	writeStatus(w, statusCode)
}

func singleHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func denialStatus(code string) int {
	switch strings.TrimSpace(code) {
	case "invalid_key", "expired", "revoked":
		return http.StatusUnauthorized
	case "rate_limited":
		return http.StatusTooManyRequests
	case "scope_denied":
		return http.StatusForbidden
	default:
		return http.StatusForbidden
	}
}

func writeCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeStatus(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
}
