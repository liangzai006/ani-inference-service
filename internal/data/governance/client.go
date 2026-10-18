// Package governance implements the owner-to-Governance release report.
package governance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	quotav1 "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var ErrNotConfigured = errors.New("Governance release reporter is not configured")

type TLSConfig struct{ CAFile, CertFile, KeyFile, ServerName string }
type Config struct {
	Endpoint string
	TLS      TLSConfig
	Timeout  time.Duration
}

func (cfg Config) Validate() error {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.TLS.CAFile) == "" || strings.TrimSpace(cfg.TLS.CertFile) == "" || strings.TrimSpace(cfg.TLS.KeyFile) == "" || strings.TrimSpace(cfg.TLS.ServerName) == "" || cfg.Timeout <= 0 {
		return ErrNotConfigured
	}
	return nil
}

type Client struct {
	release quotav1.QuotaReleaseServiceClient
	timeout time.Duration
}

var _ gpu.RefundReporter = (*Client)(nil)

// contexts is accepted for source compatibility with the composition root;
// release accounting is Governance-owned and is never loaded by Inference.
func NewClient(release quotav1.QuotaReleaseServiceClient, _ gpu.RefundContextStore, timeout time.Duration) (*Client, error) {
	if release == nil || timeout <= 0 {
		return nil, ErrNotConfigured
	}
	return &Client{release: release, timeout: timeout}, nil
}
func Dial(ctx context.Context, cfg Config, contexts gpu.RefundContextStore) (*Client, func() error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	ca, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read Governance CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, nil, errors.New("Governance CA bundle has no certificates")
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load Inference release identity: %w", err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, cfg.Endpoint, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: cfg.TLS.ServerName})), grpc.WithBlock())
	if err != nil {
		return nil, nil, fmt.Errorf("dial Governance release service: %w", err)
	}
	client, err := NewClient(quotav1.NewQuotaReleaseServiceClient(conn), contexts, cfg.Timeout)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return client, conn.Close, nil
}

func (c *Client) ReportResourceReleased(ctx context.Context, n gpu.ReleaseNotification) error {
	if c == nil || c.release == nil || c.timeout <= 0 {
		return ErrNotConfigured
	}
	if n.ReleaseEventID == "" || n.CreateOperationID == "" || n.ResourceID == "" || (n.Reason != gpu.ResourceReleased && n.Reason != gpu.AbortedCleaned) {
		return gpu.ErrInvalidRefund
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req := &quotav1.ReportResourceReleasedRequest{ReleaseEventId: n.ReleaseEventID, CreateOperationId: n.CreateOperationID, ResourceId: n.ResourceID}
	if n.Reason == gpu.ResourceReleased {
		req.Reason = quotav1.ReleaseReason_RESOURCE_RELEASED
	} else if n.Reason == gpu.AbortedCleaned {
		req.Reason = quotav1.ReleaseReason_ABORTED_CLEANED
	}
	response, err := c.release.ReportResourceReleased(callCtx, req)
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("%w: missing Governance release response", gpu.ErrInvalidRefund)
	}
	if response.GetReleaseEventId() != n.ReleaseEventID || response.GetCreateOperationId() != n.CreateOperationID || response.GetResourceId() != n.ResourceID {
		return fmt.Errorf("%w: Governance release identity mismatch", gpu.ErrInvalidRefund)
	}
	if releaseStatus := response.GetStatus(); releaseStatus != quotav1.ResourceReleaseStatus_APPLIED && releaseStatus != quotav1.ResourceReleaseStatus_ALREADY_APPLIED {
		return fmt.Errorf("%w: invalid Governance release status %s", gpu.ErrInvalidRefund, releaseStatus)
	}
	return nil
}
