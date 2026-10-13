package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	modelv1 "github.com/liangzai006/ani-inference-service/api/model/v1"
	modeldata "github.com/liangzai006/ani-inference-service/internal/data/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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

// configuredModelClient creates a Model service client with mandatory mTLS.
// When ANI_MODEL_GRPC_ADDR is empty, the Model client is disabled (returns nil).
// When ANI_MODEL_GRPC_ADDR is set, all TLS environment variables are required:
// ANI_MODEL_CA_FILE, ANI_MODEL_CERT_FILE, ANI_MODEL_KEY_FILE, ANI_MODEL_SERVER_NAME.
// This enforces secure service-to-service communication in production.
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

	// Enforce mTLS: if address is configured, all TLS params are required
	if err := tlsConfig.Validate(); err != nil {
		return nil, func() {}, fmt.Errorf("Model service requires mTLS when ANI_MODEL_GRPC_ADDR is set: %w", err)
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

	// Verify client certificate contains exactly one DNS SAN matching ani-inference
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, func() {}, fmt.Errorf("parse Inference client certificate: %w", err)
	}
	if len(leaf.DNSNames) != 1 {
		return nil, func() {}, errors.New("Inference client certificate must contain exactly one DNS SAN")
	}
	// Expected: ani-inference.ani.internal or similar registered DNS identity
	clientDNS := leaf.DNSNames[0]
	if strings.TrimSpace(clientDNS) == "" || strings.ContainsAny(clientDNS, "*:/\\ \t\n") {
		return nil, func() {}, fmt.Errorf("invalid Inference client DNS SAN: %s", clientDNS)
	}

	creds := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		ServerName:   tlsConfig.ServerName,
		// Verify server certificate contains exact DNS SAN, no wildcards
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return errors.New("unverified Model server identity")
			}
			// Check server certificate DNS SANs for exact match
			for _, name := range state.PeerCertificates[0].DNSNames {
				if name == tlsConfig.ServerName {
					return nil
				}
			}
			return fmt.Errorf("Model server DNS SAN mismatch: expected %s", tlsConfig.ServerName)
		},
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
