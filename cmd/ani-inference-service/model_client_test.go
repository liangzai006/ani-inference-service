package main

import (
	"context"
	"net"
	"strings"
	"testing"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	"google.golang.org/grpc"
)

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

type plainModelServer struct {
	modelv1.UnimplementedModelServiceServer
}

func (plainModelServer) GetModelVersion(context.Context, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	return &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{
		Id: "version-id", Status: "ready", StoragePath: "tenant/model",
		ChecksumSha256: strings.Repeat("a", 64),
	}}, nil
}

func TestConfiguredModelClientUsesPlaintextModelGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	modelv1.RegisterModelServiceServer(server, plainModelServer{})
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		listener.Close()
	})

	t.Setenv("ANI_MODEL_GRPC_ADDR", listener.Addr().String())
	client, closeClient, err := configuredModelClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeClient)
	if _, err := client.GetReadyVersion(context.Background(), "tenant", "version-id"); err != nil {
		t.Fatalf("plaintext Model gRPC call failed: %v", err)
	}
}
