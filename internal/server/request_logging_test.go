package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/metadata"
)

func TestRequestLoggingCorrelatesManagedOperation(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-ani-request-id", "rpc-1", "x-ani-origin-request-id", "web-1"))
	req := &inferencev1.CreateInferenceServiceRequest{Context: &inferencev1.CommandContext{OperationId: "operation-1"}}
	_, err := requestLogging(logger)(func(context.Context, any) (any, error) { return nil, errors.New("test failure") })(ctx, req)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"request_id=rpc-1", "origin_request_id=web-1", "operation_id=operation-1"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %s in %s", want, output.String())
		}
	}
}
