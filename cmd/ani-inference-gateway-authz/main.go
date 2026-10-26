// Command ani-inference-gateway-authz is the small HTTP adapter used by
// Higress ext-auth. It keeps the public opaque key contract at the gateway and
// delegates the authoritative decision to Inference over mTLS.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/liangzai006/ani-inference-service/internal/data/inferenceaccess"
	"github.com/liangzai006/ani-inference-service/internal/service/gatewayauthz"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("gateway authz terminated", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	timeout, err := durationEnv("ANI_GATEWAY_AUTHZ_TIMEOUT", time.Second)
	if err != nil {
		return err
	}
	client, closeClient, err := inferenceaccess.Dial(context.Background(), inferenceaccess.Config{
		Endpoint: envOrDefault("ANI_INFERENCE_GRPC_ADDR", "ani-inference-service-grpc.ani-system.svc.cluster.local:19090"),
		TLS: inferenceaccess.TLSConfig{
			CAFile:         requiredEnv("ANI_INFERENCE_CA_FILE"),
			CertFile:       requiredEnv("ANI_INFERENCE_CLIENT_CERT_FILE"),
			KeyFile:        requiredEnv("ANI_INFERENCE_CLIENT_KEY_FILE"),
			ServerName:     envOrDefault("ANI_INFERENCE_SERVER_NAME", "ani-inference.ani.internal"),
			ClientIdentity: envOrDefault("ANI_INFERENCE_CLIENT_IDENTITY", "ani-inference-gateway.ani.internal"),
		},
		Timeout: timeout,
	})
	if err != nil {
		return err
	}
	defer closeClient()

	mux := http.NewServeMux()
	authz := gatewayauthz.NewHandler(client)
	authz.RequiredSource = envOrDefault("ANI_GATEWAY_AUTHZ_SOURCE", "higress")
	mux.Handle("/authz", authz)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{
		Addr:              envOrDefault("ANI_GATEWAY_AUTHZ_ADDR", ":8080"),
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	logger.Info("gateway authz started", "version", version, "addr", server.Addr)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signals:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func requiredEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}
