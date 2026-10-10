package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	modeldata "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type ModelTLSConfig struct {
	CAFile, CertFile, KeyFile, ServerName string
}

func (c ModelTLSConfig) IsConfigured() bool {
	return c.CAFile != "" || c.CertFile != "" || c.KeyFile != "" || c.ServerName != ""
}

func (c ModelTLSConfig) Validate() error {
	if strings.TrimSpace(c.CAFile) == "" ||
		strings.TrimSpace(c.CertFile) == "" ||
		strings.TrimSpace(c.KeyFile) == "" ||
		strings.TrimSpace(c.ServerName) == "" {
		return errors.New("model TLS requires CA, cert, key and server name")
	}
	return nil
}

// configuredModelClient creates a Model service client with optional mTLS.
// When TLS environment variables are not set, it falls back to insecure credentials
// for local development. Production deployments should always configure mTLS.
func configuredModelClient() (*modeldata.Client, func(), error) {
	address := strings.TrimSpace(os.Getenv("ANI_MODEL_GRPC_ADDR"))
	if address == "" {
		return nil, func() {}, nil
	}

	tlsConfig := ModelTLSConfig{
		CAFile:     strings.TrimSpace(os.Getenv("ANI_MODEL_CA_FILE")),
		CertFile:   strings.TrimSpace(os.Getenv("ANI_MODEL_CERT_FILE")),
		KeyFile:    strings.TrimSpace(os.Getenv("ANI_MODEL_KEY_FILE")),
		ServerName: strings.TrimSpace(os.Getenv("ANI_MODEL_SERVER_NAME")),
	}

	// If no TLS config is provided, use insecure credentials (development only)
	if !tlsConfig.IsConfigured() {
		opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
		conn, err := grpc.NewClient(address, opts...)
		if err != nil {
			return nil, func() {}, fmt.Errorf("configure Model client: %w", err)
		}
		return modeldata.NewClient(modelv1.NewModelServiceClient(conn)),
			func() { _ = conn.Close() }, nil
	}

	// Production: use mTLS
	if err := tlsConfig.Validate(); err != nil {
		return nil, func() {}, fmt.Errorf("model TLS config: %w", err)
	}

	caPEM, err := os.ReadFile(tlsConfig.CAFile)
	if err != nil {
		return nil, func() {}, fmt.Errorf("read Model CA: %w", err)
	}

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, func() {}, errors.New("Model CA bundle has no certificates")
	}

	cert, err := tls.LoadX509KeyPair(tlsConfig.CertFile, tlsConfig.KeyFile)
	if err != nil {
		return nil, func() {}, fmt.Errorf("load Inference model client identity: %w", err)
	}

	creds := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		ServerName:   tlsConfig.ServerName,
	})

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
	}

	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, func() {}, fmt.Errorf("dial Model service: %w", err)
	}

	return modeldata.NewClient(modelv1.NewModelServiceClient(conn)),
		func() { _ = conn.Close() }, nil
}
