package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

type readinessReleaseStub struct{}

func (readinessReleaseStub) ReleaseManagedGPU(context.Context, inferencebiz.OperationContext) error {
	return nil
}

func TestKubernetesRuntimeConfigurationIsMandatory(t *testing.T) {
	t.Setenv("ANI_INFERENCE_NAMESPACE", "")

	_, _, err := configureKubernetesRuntime(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ANI_INFERENCE_NAMESPACE") {
		t.Fatalf("configureKubernetesRuntime() error = %v, want missing namespace", err)
	}
}

func TestManagedGPUReadinessLeavesCPUOnlyDeploymentsReady(t *testing.T) {
	if !managedGPUReadiness(false, nil) {
		t.Fatal("CPU-only deployment without managed GPU configuration must remain ready")
	}
	if managedGPUReadiness(true, nil) {
		t.Fatal("configured managed GPU without release provider must stay not ready")
	}
	if !managedGPUReadiness(true, readinessReleaseStub{}) {
		t.Fatal("configured managed GPU with release provider must be ready")
	}
}

func TestConfiguredGPUResolverRequiresClusterID(t *testing.T) {
	t.Setenv("ANI_ACCELERATOR_GRPC_ADDR", "accelerator.example:443")
	t.Setenv("ANI_ACCELERATOR_CLUSTER_ID", "")

	_, err := configuredGPUResolver()
	if err == nil || !strings.Contains(err.Error(), "ANI_ACCELERATOR_CLUSTER_ID") {
		t.Fatalf("configuredGPUResolver() error = %v, want missing cluster id", err)
	}
}

func TestRuntimeLoggerUsesKratosRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := newRuntimeLogger(&output)
	logger.Info("redaction check", "token", "secret-token", "args", "secret-payload")

	line := output.String()
	if strings.Contains(line, "secret-token") || strings.Contains(line, "secret-payload") {
		t.Fatalf("runtime logger leaked filtered values: %s", line)
	}
	// Count the redaction marker without quotes so the assertion holds for
	// both the text and JSON encodings.
	if strings.Count(line, "***") != 2 {
		t.Fatalf("runtime logger did not use Kratos key filtering: %s", line)
	}
}

func TestRuntimeLoggerDefaultsToTextAndHonorsJSONOverride(t *testing.T) {
	t.Setenv("ANI_LOG_FORMAT", "")
	var text bytes.Buffer
	newRuntimeLogger(&text).Info("format check")
	if line := text.String(); !strings.Contains(line, "msg=\"format check\"") {
		t.Fatalf("default log format is not text: %q", line)
	}

	t.Setenv("ANI_LOG_FORMAT", "json")
	var structured bytes.Buffer
	newRuntimeLogger(&structured).Info("format check")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(structured.Bytes()), &record); err != nil {
		t.Fatalf("ANI_LOG_FORMAT=json did not produce JSON: %v; output=%q", err, structured.String())
	}
	if msg, _ := record["msg"].(string); msg != "format check" {
		t.Fatalf("JSON log msg = %q, want %q", msg, "format check")
	}
}

func TestRuntimeLoggerIncludesProcessIdentityAndSource(t *testing.T) {
	originalID, originalName, originalVersion := id, Name, Version
	id, Name, Version = "layout-test-instance", "ani-inference-service", "layout-test-version"
	t.Cleanup(func() { id, Name, Version = originalID, originalName, originalVersion })

	// Assert against JSON so identity and caller source stay machine-checkable;
	// the text encoding carries the same attributes.
	t.Setenv("ANI_LOG_FORMAT", "json")
	var output bytes.Buffer
	newRuntimeLogger(&output).Info("identity check")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatalf("decode structured log: %v; output=%q", err, output.String())
	}
	for key, want := range map[string]string{
		"msg":             "identity check",
		"service.id":      "layout-test-instance",
		"service.name":    "ani-inference-service",
		"service.version": "layout-test-version",
	} {
		if got, _ := record[key].(string); got != want {
			t.Fatalf("log field %s = %q, want %q; record=%v", key, got, want, record)
		}
	}
	if timestamp, _ := record["time"].(string); timestamp == "" {
		t.Fatalf("structured log has no timestamp: %v", record)
	}
	source, ok := record["source"].(map[string]any)
	if !ok || source["file"] == "" || source["line"] == nil {
		t.Fatalf("structured log has no caller source: %v", record)
	}
}
