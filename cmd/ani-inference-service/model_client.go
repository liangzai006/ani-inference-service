package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	modeldata "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Connection ownership stays in the process composition root. Authentication
// remains the configured transport's responsibility, not request tenant fields.
func configuredModelClient() (*modeldata.Client, func(), error) {
	address := strings.TrimSpace(os.Getenv("ANI_MODEL_GRPC_ADDR"))
	if address == "" {
		return nil, func() {}, nil
	}
	var creds credentials.TransportCredentials = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: os.Getenv("ANI_MODEL_GRPC_SERVER_NAME")})
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, func() {}, fmt.Errorf("configure Model client: %w", err)
	}
	return modeldata.NewClient(modelv1.NewModelServiceClient(conn)), func() { _ = conn.Close() }, nil
}
