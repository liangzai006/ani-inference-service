package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestInitialReadinessDoesNotAcceptPersistedCommandsWithoutWorker(t *testing.T) {
	if initialReadiness(true, true, true, true, false) {
		t.Fatal("PostgreSQL-backed process without a durable worker must not be initially ready")
	}
}

func TestInitialReadinessAllowsDependencyFreeProcess(t *testing.T) {
	if !initialReadiness(false, false, false, false, false) {
		t.Fatal("dependency-free process should retain its process readiness contract")
	}
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release listener address: %v", err)
	}
	return address
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("HTTP endpoint did not become ready: %s", url)
}

func assertProductionGRPCHealth(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		t.Fatalf("dial gRPC health endpoint: %v", err)
	}
	defer conn.Close()
	response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("check gRPC health endpoint: %v", err)
	}
	if response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("gRPC health status = %s, want SERVING", response.GetStatus())
	}
}
