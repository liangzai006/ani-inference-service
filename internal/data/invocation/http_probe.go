// Package invocation contains data-plane health adapters. Endpoint ownership
// stays outside Inference; callers must provide an explicit resolver.
package invocation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

// EndpointResolver resolves the already published endpoint for one runtime.
// It must not infer a port from an image or silently fall back to a default.
type EndpointResolver func(kubernetes.RuntimeSpec) (string, error)

// HTTPProbe performs a bounded HTTP health request against an explicitly
// resolved invocation endpoint. Transport failures are unknown because no
// reliable health result was observed; an HTTP non-2xx response is known but
// unhealthy.
type HTTPProbe struct {
	Resolver EndpointResolver
	Client   *http.Client
}

func (p HTTPProbe) Probe(ctx context.Context, spec kubernetes.RuntimeSpec) (kubernetes.InvocationResult, error) {
	if p.Resolver == nil {
		return kubernetes.InvocationResult{}, fmt.Errorf("invocation endpoint resolver is not configured")
	}
	endpoint, err := p.Resolver(spec)
	if err != nil {
		return kubernetes.InvocationResult{}, err
	}
	if endpoint == "" {
		return kubernetes.InvocationResult{}, fmt.Errorf("invocation endpoint is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return kubernetes.InvocationResult{}, err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return kubernetes.InvocationResult{Known: false, Reason: "invocation probe transport failed"}, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return kubernetes.InvocationResult{Known: true, Healthy: false, Reason: fmt.Sprintf("invocation probe returned HTTP %d", resp.StatusCode)}, nil
	}
	return kubernetes.InvocationResult{Known: true, Healthy: true, Reason: "invocation probe succeeded"}, nil
}
