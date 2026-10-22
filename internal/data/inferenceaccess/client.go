// Package inferenceaccess is the mTLS client used by the gateway authz
// adapter. It has no access to PostgreSQL or the Inference domain repository.
package inferenceaccess

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/service/gatewayauthz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var ErrInvalidConfig = errors.New("invalid Inference access-key client configuration")

type TLSConfig struct {
	CAFile         string
	CertFile       string
	KeyFile        string
	ServerName     string
	ClientIdentity string
}

type Config struct {
	Endpoint string
	TLS      TLSConfig
	Timeout  time.Duration
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(c.TLS.CAFile) == "" ||
		strings.TrimSpace(c.TLS.CertFile) == "" || strings.TrimSpace(c.TLS.KeyFile) == "" ||
		strings.TrimSpace(c.TLS.ServerName) == "" || strings.TrimSpace(c.TLS.ClientIdentity) == "" || c.Timeout <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

type Client struct {
	accessKey inferencev1.InferenceAccessKeyServiceClient
	timeout   time.Duration
}

var _ gatewayauthz.Checker = (*Client)(nil)

func NewClient(accessKey inferencev1.InferenceAccessKeyServiceClient, timeout time.Duration) (*Client, error) {
	if accessKey == nil || timeout <= 0 {
		return nil, ErrInvalidConfig
	}
	return &Client{accessKey: accessKey, timeout: timeout}, nil
}

func Dial(ctx context.Context, cfg Config) (*Client, func() error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	caPEM, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read Inference CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, nil, fmt.Errorf("%w: CA bundle has no certificates", ErrInvalidConfig)
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load gateway authz client certificate: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, nil, fmt.Errorf("%w: client certificate chain is empty", ErrInvalidConfig)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("parse gateway authz client certificate: %w", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != cfg.TLS.ClientIdentity || len(leaf.URIs) != 0 || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 {
		return nil, nil, fmt.Errorf("%w: client certificate identity must be exactly %q", ErrInvalidConfig, cfg.TLS.ClientIdentity)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{cert},
		ServerName:   cfg.TLS.ServerName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return fmt.Errorf("%w: server certificate is not verified", ErrInvalidConfig)
			}
			for _, name := range state.PeerCertificates[0].DNSNames {
				if name == cfg.TLS.ServerName {
					return nil
				}
			}
			return fmt.Errorf("%w: server DNS SAN mismatch, expected %q", ErrInvalidConfig, cfg.TLS.ServerName)
		},
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, cfg.Endpoint, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithBlock())
	if err != nil {
		return nil, nil, fmt.Errorf("dial Inference access-key service: %w", err)
	}
	client, err := NewClient(inferencev1.NewInferenceAccessKeyServiceClient(conn), cfg.Timeout)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return client, conn.Close, nil
}

func (c *Client) Check(ctx context.Context, input gatewayauthz.CheckRequest) (gatewayauthz.CheckResult, error) {
	if c == nil || c.accessKey == nil || c.timeout <= 0 {
		return gatewayauthz.CheckResult{}, ErrInvalidConfig
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	response, err := c.accessKey.CheckInferenceAccess(callCtx, &inferencev1.CheckInferenceAccessRequest{
		ApiKey: input.APIKey, ServiceId: input.ServiceID, ServedModelName: input.ServedModelName,
		RequestCost: input.RequestCost, RequestId: input.RequestID,
	})
	if err != nil {
		return gatewayauthz.CheckResult{}, err
	}
	if response == nil {
		return gatewayauthz.CheckResult{}, errors.New("Inference returned an empty access decision")
	}
	return gatewayauthz.CheckResult{
		Allowed: response.GetAllowed(), TenantID: response.GetTenantId(), InferenceKeyID: response.GetKeyId(),
		DenialCode: response.GetDenialCode(), RetryAfterSeconds: response.GetRetryAfterSeconds(), PolicyVersion: response.GetPolicyVersion(),
	}, nil
}
