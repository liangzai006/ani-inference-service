package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// higressConsoleStatusError preserves the HTTP status for callers that need
// to distinguish an absent resource from other Console failures.
type higressConsoleStatusError struct {
	Method     string
	Path       string
	Status     int
	StatusCode int
}

func (e *higressConsoleStatusError) Error() string {
	return fmt.Sprintf("Higress Console API %s %s: status %d", e.Method, e.Path, e.Status)
}

// higressConsoleClient is the small transport adapter used for Higress Console's
// native management API. Request payloads stay at this boundary as maps so the
// business layer does not depend on Console API types.
type higressConsoleClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func newHigressConsoleClient(baseURL, username, password string, httpClient *http.Client) *higressConsoleClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &higressConsoleClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     httpClient,
	}
}

func (c *higressConsoleClient) request(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("Higress Console API %s %s: encode request: %w", method, path, err)
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return nil, 0, fmt.Errorf("Higress Console API %s %s: create request: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Higress Console API %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("Higress Console API %s %s: read response: %w", method, path, readErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseBody, resp.StatusCode, &higressConsoleStatusError{Method: method, Path: path, Status: resp.StatusCode, StatusCode: resp.StatusCode}
	}
	if err := higressConsoleEnvelopeError(method, path, resp.StatusCode, responseBody); err != nil {
		return responseBody, resp.StatusCode, err
	}
	return responseBody, resp.StatusCode, nil
}

// higressConsoleEnvelopeError handles application-level failures reported in a
// successful HTTP response. The response message is intentionally ignored so
// Console internals or credentials cannot leak through the adapter error.
func higressConsoleEnvelopeError(method, path string, status int, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	var envelope struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Success == nil || *envelope.Success {
		return nil
	}
	return fmt.Errorf("Higress Console API %s %s: status %d", method, path, status)
}

func (c *higressConsoleClient) GetRoute(ctx context.Context, name string) (map[string]any, error) {
	path := "/v1/routes/" + url.PathEscape(name)
	body, _, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success *bool          `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("Higress Console API GET %s: decode response: %w", path, err)
	}
	if envelope.Success == nil {
		return nil, fmt.Errorf("Higress Console API GET %s: decode response: missing success", path)
	}
	return envelope.Data, nil
}

func (c *higressConsoleClient) PutRoute(ctx context.Context, name string, route map[string]any) error {
	path := "/v1/routes/" + url.PathEscape(name)
	_, _, err := c.request(ctx, http.MethodPut, path, route)
	return err
}

func (c *higressConsoleClient) DeleteRoute(ctx context.Context, name string) error {
	return c.delete(ctx, "/v1/routes/"+url.PathEscape(name))
}

func (c *higressConsoleClient) ListRoutes(ctx context.Context) ([]map[string]any, error) {
	const path = "/v1/routes"
	body, _, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success *bool            `json:"success"`
		Data    []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("Higress Console API GET %s: decode response: %w", path, err)
	}
	if envelope.Success == nil {
		return nil, fmt.Errorf("Higress Console API GET %s: decode response: missing success", path)
	}
	return envelope.Data, nil
}

func (c *higressConsoleClient) PutServiceSource(ctx context.Context, name string, source map[string]any) error {
	path := "/v1/service-sources/" + url.PathEscape(name)
	_, _, err := c.request(ctx, http.MethodPut, path, source)
	return err
}

func (c *higressConsoleClient) DeleteServiceSource(ctx context.Context, name string) error {
	return c.delete(ctx, "/v1/service-sources/"+url.PathEscape(name))
}

func (c *higressConsoleClient) delete(ctx context.Context, path string) error {
	_, status, err := c.request(ctx, http.MethodDelete, path, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}
