package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *RuntimeDiagnostics) ProbeInvocation(ctx context.Context, scope biz.Scope, model string) (biz.InvocationHealth, error) {
	runtime, pods, err := d.ownedPods(ctx, scope)
	if err != nil {
		return biz.InvocationHealth{}, err
	}
	if runtime.DesiredState != "running" || !runtime.PublicationPublished {
		return biz.InvocationHealth{State: "unhealthy", Reason: "endpoint_not_ready"}, nil
	}
	port := int32(8000)
	if runtime.Endpoint != nil {
		port = runtime.Endpoint.ContainerPort
	}
	if port < 1 || port > 65535 || model == "" {
		return biz.InvocationHealth{State: "unhealthy", Reason: "invocation_probe_not_configured"}, nil
	}
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != "Running" || net.ParseIP(pod.Status.PodIP) == nil {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				ready = true
			}
		}
		if !ready {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"model": model, "prompt": "health", "max_tokens": 1, "stream": false})
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		req, e := http.NewRequestWithContext(probeCtx, http.MethodPost, "http://"+net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(int(port)))+"/v1/completions", bytes.NewReader(payload))
		if e != nil {
			cancel()
			return biz.InvocationHealth{}, e
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, e := client.Do(req)
		result := biz.InvocationHealth{State: "unhealthy", Reason: "invocation_probe_failed", ObservedAt: time.Now().UTC()}
		if e == nil {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
			response.Body.Close()
			var body struct {
				Choices []struct {
					Text string `json:"text"`
				} `json:"choices"`
			}
			if response.StatusCode == 200 && readErr == nil && len(raw) <= 64*1024 && json.Unmarshal(raw, &body) == nil && len(body.Choices) > 0 {
				// A completion can legitimately contain a whitespace token. The presence
				// of text (including whitespace) is the evidence; an empty body is not.
				if body.Choices[0].Text != "" {
					result.State = "healthy"
					result.Reason = "runtime_invocation_succeeded"
				}
			}
		}
		cancel()
		if ctx.Err() != nil {
			return biz.InvocationHealth{}, ctx.Err()
		}
		actual, e := d.Core.Pods(runtime.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if e != nil || actual.UID != pod.UID || actual.DeletionTimestamp != nil {
			return biz.InvocationHealth{}, fmt.Errorf("runtime changed during invocation probe")
		}
		current, e := d.Source.CurrentRuntime(ctx, scope.TenantID, scope.ServiceID, scope.Generation)
		if e != nil || current.Generation != scope.Generation || strings.ToLower(current.DesiredState) != "running" || !current.PublicationPublished {
			return biz.InvocationHealth{State: "unknown", Reason: "runtime_changed_during_probe"}, nil
		}
		return result, nil
	}
	return biz.InvocationHealth{State: "unhealthy", Reason: "no_ready_invocation_instance"}, nil
}
