package main

import "testing"

func TestModelClientOptionalConfiguration(t *testing.T) {
	t.Setenv("ANI_MODEL_GRPC_ADDR", "")
	client, closeClient, err := configuredModelClient()
	if err != nil || client != nil {
		t.Fatalf("unconfigured client=%v err=%v", client, err)
	}
	closeClient()
	t.Setenv("ANI_MODEL_GRPC_ADDR", "localhost:9090")
	client, closeClient, err = configuredModelClient()
	if err != nil || client == nil {
		t.Fatalf("configured client=%v err=%v", client, err)
	}
	closeClient()
}
