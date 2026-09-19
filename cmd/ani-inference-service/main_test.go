package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRuntimeLoggerUsesKratosRedaction(t *testing.T) {
	var output bytes.Buffer
	logger := newRuntimeLogger(&output)
	logger.Info("redaction check", "token", "secret-token", "args", "secret-payload")

	line := output.String()
	if strings.Contains(line, "secret-token") || strings.Contains(line, "secret-payload") {
		t.Fatalf("runtime logger leaked filtered values: %s", line)
	}
	if strings.Count(line, `"***"`) != 2 {
		t.Fatalf("runtime logger did not use Kratos key filtering: %s", line)
	}
}

func TestRuntimeLoggerIncludesProcessIdentityAndSource(t *testing.T) {
	originalID, originalName, originalVersion := id, Name, Version
	id, Name, Version = "layout-test-instance", "ani-inference-service", "layout-test-version"
	t.Cleanup(func() { id, Name, Version = originalID, originalName, originalVersion })

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
