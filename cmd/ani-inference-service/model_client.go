package main

import (
	"fmt"
	"os"
	"strings"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	modeldata "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Connection ownership stays in the process composition root. The isolated
// validation cluster uses its internal plaintext Model gRPC Service directly.
func configuredModelClient() (*modeldata.Client, func(), error) {
	address := strings.TrimSpace(os.Getenv("ANI_MODEL_GRPC_ADDR"))
	if address == "" {
		return nil, func() {}, nil
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, func() {}, fmt.Errorf("configure Model client: %w", err)
	}
	return modeldata.NewClient(modelv1.NewModelServiceClient(conn)), func() { _ = conn.Close() }, nil
}
