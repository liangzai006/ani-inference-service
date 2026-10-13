package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

// InferenceServerTLSConfig is the mandatory TLS material for the ordinary
// Inference gRPC listener. It is separate from managed_gpu.receiver because
// that listener has a dedicated address and Governance attachment contract.
type InferenceServerTLSConfig struct {
	CAFile, CertFile, KeyFile string
}

func (c InferenceServerTLSConfig) Validate() error {
	if strings.TrimSpace(c.CAFile) == "" || strings.TrimSpace(c.CertFile) == "" || strings.TrimSpace(c.KeyFile) == "" {
		return errors.New("mTLS requires ANI_SERVER_TLS_CA_FILE, ANI_SERVER_TLS_CERT_FILE, and ANI_SERVER_TLS_KEY_FILE")
	}
	return nil
}

func configuredInferenceGRPCServer(c *inferencev1.Server_GRPC, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	if c == nil {
		return nil, errors.New("server.grpc config is required")
	}
	settings := InferenceServerTLSConfig{
		CAFile:   strings.TrimSpace(os.Getenv("ANI_SERVER_TLS_CA_FILE")),
		CertFile: strings.TrimSpace(os.Getenv("ANI_SERVER_TLS_CERT_FILE")),
		KeyFile:  strings.TrimSpace(os.Getenv("ANI_SERVER_TLS_KEY_FILE")),
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(settings.CertFile, settings.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load Inference server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(settings.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read Inference server CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("Inference server CA bundle has no certificates")
	}

	config := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(verifiedChains) == 0 || len(verifiedChains[0]) == 0 {
				return errors.New("unverified client certificate chain")
			}
			clientCert := verifiedChains[0][0]
			if len(clientCert.DNSNames) != 1 {
				return errors.New("client certificate must contain exactly one DNS SAN")
			}
			if strings.TrimSpace(clientCert.DNSNames[0]) == "" || strings.ContainsAny(clientCert.DNSNames[0], "*:/\\ \t\n") {
				return fmt.Errorf("invalid client DNS SAN: %s", clientCert.DNSNames[0])
			}
			return nil
		},
	}

	all := append([]middleware.Middleware{trustedTenantMiddleware()}, middlewares...)
	return kratosgrpc.NewServer(
		kratosgrpc.Network(c.Network),
		kratosgrpc.Address(c.Addr),
		kratosgrpc.Timeout(c.Timeout.AsDuration()),
		kratosgrpc.TLSConfig(config),
		kratosgrpc.Middleware(all...),
		kratosgrpc.DisableReflection(),
	), nil
}
