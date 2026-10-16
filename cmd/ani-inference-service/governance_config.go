package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/data/governance"
)

// governanceRefundConfig contains only the outbound refund client. Inference
// has one inbound gRPC listener; GPU commands use the ordinary listener after
// the transport middleware binds the verified Governance tenant context.
func governanceRefundConfig(managed *inferencev1.ManagedGPU) (governance.Config, bool, error) {
	refund := managed.GetRefund()
	cfg := governance.Config{
		Endpoint: strings.TrimSpace(refund.GetAddress()),
		TLS: governance.TLSConfig{
			CAFile:     strings.TrimSpace(refund.GetCaFile()),
			CertFile:   strings.TrimSpace(refund.GetCertFile()),
			KeyFile:    strings.TrimSpace(refund.GetKeyFile()),
			ServerName: strings.TrimSpace(refund.GetServerName()),
		},
		Timeout: 5 * time.Second,
	}
	enabled := cfg.Endpoint != "" || cfg.TLS.CAFile != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.ServerName != ""
	if !enabled {
		return cfg, false, nil
	}
	if refund.GetTimeout() != nil {
		if err := refund.GetTimeout().CheckValid(); err != nil || refund.GetTimeout().AsDuration() <= 0 {
			return cfg, true, errors.New("managed_gpu.refund.timeout must be a positive duration")
		}
		cfg.Timeout = refund.GetTimeout().AsDuration()
	}
	if err := cfg.Validate(); err != nil {
		return cfg, true, fmt.Errorf("managed GPU refund requires address, CA, certificate, key and server name: %w", err)
	}
	return cfg, true, nil
}

func configuredGovernanceRefundReporter(managed *inferencev1.ManagedGPU, store gpu.RefundContextStore) (gpu.RefundReporter, func(), error) {
	cfg, enabled, err := governanceRefundConfig(managed)
	if err != nil {
		return nil, func() {}, err
	}
	if !enabled {
		return nil, func() {}, nil
	}
	client, closeClient, err := governance.Dial(context.Background(), cfg, store)
	if err != nil {
		return nil, func() {}, fmt.Errorf("configure Governance quota client: %w", err)
	}
	return client, func() { _ = closeClient() }, nil
}
