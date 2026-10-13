package main

import (
	"testing"
)

func TestModelClientOptionalConfiguration(t *testing.T) {
	t.Setenv("ANI_MODEL_GRPC_ADDR", "")
	client, closeClient, err := configuredModelClient()
	if err != nil || client != nil {
		t.Fatalf("unconfigured client=%v err=%v", client, err)
	}
	closeClient()
	t.Setenv("ANI_MODEL_GRPC_ADDR", "localhost:9090")
	t.Setenv("ANI_MODEL_CA_FILE", "")
	t.Setenv("ANI_MODEL_CERT_FILE", "")
	t.Setenv("ANI_MODEL_KEY_FILE", "")
	t.Setenv("ANI_MODEL_SERVER_NAME", "")
	client, closeClient, err = configuredModelClient()
	if err == nil || client != nil {
		t.Fatalf("plaintext configuration client=%v err=%v", client, err)
	}
	closeClient()
}

func TestConfiguredModelClientRejectsPlaintextModelGRPC(t *testing.T) {
	t.Setenv("ANI_MODEL_GRPC_ADDR", "127.0.0.1:19090")
	t.Setenv("ANI_MODEL_CA_FILE", "")
	t.Setenv("ANI_MODEL_CERT_FILE", "")
	t.Setenv("ANI_MODEL_KEY_FILE", "")
	t.Setenv("ANI_MODEL_SERVER_NAME", "")
	client, closeClient, err := configuredModelClient()
	if err == nil || client != nil {
		t.Fatalf("plaintext client=%v err=%v", client, err)
	}
	closeClient()
}
