package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/data/governance"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func governanceRefundConfig(managed *inferencev1.ManagedGPU) (governance.Config, bool, error) {
	refund := managed.GetRefund()
	receiver := managed.GetReceiver()
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
	enabled := cfg.Endpoint != "" || cfg.TLS.CAFile != "" || cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" || cfg.TLS.ServerName != "" || receiver.GetCaFile() != "" || receiver.GetCertFile() != "" || receiver.GetKeyFile() != "" || receiver.GetAddress() != ""
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
	if strings.TrimSpace(receiver.GetAddress()) == "" || receiver.GetCaFile() == "" || receiver.GetCertFile() == "" || receiver.GetKeyFile() == "" {
		return cfg, true, errors.New("managed_gpu.receiver requires address, CA, certificate and key")
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
	if _, configured, err := governanceReceiverTLS(managed); err != nil || !configured {
		if err != nil {
			return nil, func() {}, err
		}
		return nil, func() {}, errors.New("managed GPU refund requires the trusted Governance receiver TLS configuration")
	}
	client, closeClient, err := governance.Dial(context.Background(), cfg, store)
	if err != nil {
		return nil, func() {}, fmt.Errorf("configure Governance quota client: %w", err)
	}
	return client, func() { _ = closeClient() }, nil
}

func governanceReceiverTLS(managed *inferencev1.ManagedGPU) (*tls.Config, bool, error) {
	_, enabled, err := governanceRefundConfig(managed)
	if err != nil {
		return nil, enabled, err
	}
	if !enabled {
		return nil, false, nil
	}
	receiver := managed.GetReceiver()
	caFile := strings.TrimSpace(receiver.GetCaFile())
	certFile := strings.TrimSpace(receiver.GetCertFile())
	keyFile := strings.TrimSpace(receiver.GetKeyFile())
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, true, fmt.Errorf("read Governance receiver CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, true, errors.New("Governance receiver CA bundle has no certificates")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, true, fmt.Errorf("load Inference receiver identity: %w", err)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs: roots, Certificates: []tls.Certificate{cert},
		VerifyPeerCertificate: func(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(verifiedChains) == 0 || len(verifiedChains[0]) == 0 {
				return errors.New("unverified client certificate chain")
			}
			_, err := clientDNSIdentity(verifiedChains[0][0])
			return err
		},
	}, true, nil
}

func configuredGovernanceGRPCServer(c *inferencev1.Server_GRPC, managed *inferencev1.ManagedGPU, owner inferencev1.InferenceServiceManagerServer, middlewares ...middleware.Middleware) (*kratosgrpc.Server, error) {
	tlsConfig, enabled, err := governanceReceiverTLS(managed)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, nil
	}
	// Authenticate and bind the attachment tenant before generic metadata
	// middleware. The latter still rejects mismatched legacy tenant aliases.
	all := append([]middleware.Middleware{governanceIdentityMiddleware()}, middlewares...)
	grpcServer := kratosgrpc.NewServer(
		kratosgrpc.Network(c.Network), kratosgrpc.Address(managed.GetReceiver().GetAddress()),
		kratosgrpc.Timeout(c.Timeout.AsDuration()), kratosgrpc.TLSConfig(tlsConfig),
		kratosgrpc.Middleware(all...), kratosgrpc.DisableReflection(),
	)
	inferencev1.RegisterInferenceServiceManagerServer(grpcServer, owner)
	return grpcServer, nil
}

func governanceIdentityMiddleware() middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request interface{}) (interface{}, error) {
			identity, err := extractClientIdentity(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Unauthenticated, "verified Governance identity is required: %v", err)
			}
			if identity != governanceCallerDNS {
				return nil, status.Error(codes.PermissionDenied, "unregistered Governance caller")
			}
			ctx = gpu.WithGovernanceIdentity(ctx)
			tenant := ""
			switch req := request.(type) {
			case *inferencev1.CreateInferenceServiceRequest:
				if req.GetGpuOwnerAttachment() != nil {
					tenant = req.GetGpuOwnerAttachment().GetRef().GetTenantId()
				}
			case *inferencev1.DeleteInferenceServiceRequest:
				if req.GetGpuOwnerAttachment() != nil {
					tenant = req.GetGpuOwnerAttachment().GetRef().GetTenantId()
				}
			}
			if tenant != "" {
				if md, ok := metadata.FromIncomingContext(ctx); ok {
					for _, key := range []string{"x-ani-tenant-id", "tenant-id", "x-tenant-id", "x-md-tenant-id"} {
						for _, value := range md.Get(key) {
							if strings.TrimSpace(value) != tenant {
								return nil, status.Error(codes.PermissionDenied, "tenant metadata differs from authenticated owner attachment")
							}
						}
					}
				}
				if bound, ok := service.TenantID(ctx); ok && bound != tenant {
					return nil, status.Error(codes.PermissionDenied, "trusted tenant differs from owner attachment")
				}
				ctx = service.WithTenantID(ctx, tenant)
			}
			return next(ctx, request)
		}
	}
}
