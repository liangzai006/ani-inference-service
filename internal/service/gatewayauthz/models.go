package gatewayauthz

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelsResult struct {
	Allowed           bool
	DenialCode        string
	RetryAfterSeconds int32
	Models            []Model
}

type ModelsLister interface {
	ListModels(context.Context, string) (ModelsResult, error)
}

// ModelsHandler authenticates the public listing itself. No client-supplied
// model name, tenant header or prior ext-auth decision establishes its scope.
type ModelsHandler struct{ Lister ModelsLister }

func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	writeCommonHeaders(w)
	if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
		writeStatus(w, http.StatusNotFound)
		return
	}
	key, ok := inferenceCredential(r.Header)
	if !ok {
		writeStatus(w, http.StatusUnauthorized)
		return
	}
	if h == nil || h.Lister == nil {
		writeStatus(w, http.StatusServiceUnavailable)
		return
	}
	result, err := h.Lister.ListModels(r.Context(), key)
	if err != nil {
		writeStatus(w, http.StatusServiceUnavailable)
		return
	}
	if !result.Allowed {
		status := denialStatus(result.DenialCode)
		if status == http.StatusTooManyRequests && result.RetryAfterSeconds > 0 {
			w.Header().Set(HeaderRetryAfter, strconv.Itoa(int(result.RetryAfterSeconds)))
		}
		writeStatus(w, status)
		return
	}
	models := result.Models
	if models == nil {
		models = []Model{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Object string  `json:"object"`
		Data   []Model `json:"data"`
	}{Object: "list", Data: models})
}

func isForwardedModelListing(header http.Header) bool {
	method, ok := singleHeader(header, "x-forwarded-method")
	if !ok || method != http.MethodGet {
		return false
	}
	uri, ok := singleHeader(header, "x-forwarded-uri")
	if !ok {
		return false
	}
	parsed, err := url.ParseRequestURI(uri)
	return err == nil && parsed.Path == "/v1/models" && parsed.RawPath == "" && !parsed.IsAbs()
}
