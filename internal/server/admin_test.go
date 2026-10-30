package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kratoslog "github.com/go-kratos/kratos/v3/log"
	kratosmetadata "github.com/go-kratos/kratos/v3/metadata"
	kratoshttp "github.com/go-kratos/kratos/v3/transport/http"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/liangzai006/ani-inference-service/internal/conf"
)

type invalidRequest struct{}

func (invalidRequest) Validate() error { return errors.New("invalid test request") }

func TestAdminServerRunsKratosMiddlewareChain(t *testing.T) {
	readiness := NewReadiness()
	observability, err := NewObservability("ani-inference-service-test", "test", readiness)
	if err != nil {
		t.Fatalf("NewObservability() error = %v", err)
	}
	defer observability.Shutdown(context.Background())
	logger := slog.New(kratoslog.NewHandler(kratoslog.WithWriter(io.Discard)))
	admin := NewAdminServer(
		&conf.Server_Admin{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(time.Second)},
		readiness,
		observability.Gatherer(),
		observability.ServerMiddleware(logger)...,
	)
	router := admin.Route("/")
	router.GET("metadata", func(ctx kratoshttp.Context) error {
		return invokeWithRequest(ctx, struct{}{}, func(inner context.Context, _ any) (any, error) {
			md, ok := kratosmetadata.FromServerContext(inner)
			if !ok || md.Get("x-md-caller") != "layout-test" {
				return nil, errors.New("Kratos metadata middleware did not propagate x-md-caller")
			}
			return map[string]string{"status": "ok"}, nil
		})
	})
	router.GET("validate", func(ctx kratoshttp.Context) error {
		return invokeWithRequest(ctx, invalidRequest{}, func(context.Context, any) (any, error) {
			return map[string]string{"status": "unexpected"}, nil
		})
	})
	router.GET("panic", func(ctx kratoshttp.Context) error {
		return invoke(ctx, func(context.Context, any) (any, error) {
			panic("middleware recovery test")
		})
	})

	metadataResponse := httptest.NewRecorder()
	metadataRequest := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	metadataRequest.Header.Set("x-md-caller", "layout-test")
	admin.ServeHTTP(metadataResponse, metadataRequest)
	if metadataResponse.Code != http.StatusOK || !strings.Contains(metadataResponse.Body.String(), `"status":"ok"`) {
		t.Fatalf("metadata route = %d %q", metadataResponse.Code, metadataResponse.Body.String())
	}

	validationResponse := httptest.NewRecorder()
	admin.ServeHTTP(validationResponse, httptest.NewRequest(http.MethodGet, "/validate", nil))
	if validationResponse.Code != http.StatusBadRequest || !strings.Contains(validationResponse.Body.String(), `"reason":"VALIDATOR"`) {
		t.Fatalf("validate route = %d %q", validationResponse.Code, validationResponse.Body.String())
	}

	recoveryResponse := httptest.NewRecorder()
	admin.ServeHTTP(recoveryResponse, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if recoveryResponse.Code != http.StatusInternalServerError || !strings.Contains(recoveryResponse.Body.String(), `"reason":"UNKNOWN"`) {
		t.Fatalf("panic route = %d %q", recoveryResponse.Code, recoveryResponse.Body.String())
	}
}

func TestAdminProbeLoggingKeepsFailures(t *testing.T) {
	readiness := NewReadiness()
	obs, err := NewObservability("probe-test", "test", readiness)
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Shutdown(context.Background())
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	admin := NewAdminServer(&conf.Server_Admin{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(time.Second)}, readiness, obs.Gatherer(), obs.ServerMiddleware(logger)...)
	for _, path := range []string{"/healthz", "/metrics"} {
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	readiness.Set(true)
	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || output.Len() != 0 {
		t.Fatalf("successful probe logged: %s", output.String())
	}
	readiness.Set(false)
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(output.String(), "operation=/readyz") {
		t.Fatalf("failed probe missing: %d %s", rec.Code, output.String())
	}
}
