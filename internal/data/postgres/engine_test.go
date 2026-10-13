package postgres

import "testing"

func TestEngineSpecJSONPreservesRawCommand(t *testing.T) {
	want := `/usr/bin/env VLLM_USE_V1=1 vllm serve '/models with spaces' --served-model-name custom-qwen`
	raw, err := marshalEngineSpec(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalEngineSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("engine command = %q; want %q", got, want)
	}
}

func TestEngineSpecJSONReadsLegacyWrapper(t *testing.T) {
	command, err := unmarshalEngineSpec([]byte(`{"command":["/bin/sh","-c"],"args":["vllm serve /models --served-model-name qwen"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if command != "vllm serve /models --served-model-name qwen" {
		t.Fatalf("legacy engine command = %q", command)
	}
}

func TestEngineSpecJSONRejectsArbitraryArgv(t *testing.T) {
	if _, err := unmarshalEngineSpec([]byte(`[
    "vllm", "serve", "/models"
]`)); err == nil {
		t.Fatal("arbitrary argv array accepted")
	}
}

func TestEngineSpecJSONRejectsNonShellLegacyWrapper(t *testing.T) {
	if _, err := unmarshalEngineSpec([]byte(`{"command":["vllm","serve"],"args":["/models"]}`)); err == nil {
		t.Fatal("non-shell legacy wrapper accepted")
	}
}
