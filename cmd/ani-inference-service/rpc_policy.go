package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strings"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/transport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const governanceCallerDNS = "ani-governance"

// clientDNSIdentity is shared by both TLS listeners and RPC authentication.
// ANI client identities have exactly one DNS SAN and no other SAN types.
func clientDNSIdentity(cert *x509.Certificate) (string, error) {
	if cert == nil || len(cert.DNSNames) != 1 {
		return "", fmt.Errorf("client certificate must have exactly one DNS SAN")
	}
	if len(cert.URIs) != 0 || len(cert.IPAddresses) != 0 || len(cert.EmailAddresses) != 0 {
		return "", fmt.Errorf("client certificate must not contain URI, IP or email SANs")
	}
	identity := cert.DNSNames[0]
	if identity == "" || strings.ContainsAny(identity, "*:/\\ \t\r\n") {
		return "", fmt.Errorf("invalid client DNS SAN")
	}
	return identity, nil
}

// RPCPolicyMiddleware enforces method-level authorization based on the
// verified TLS client certificate identity. This implements deny-by-default:
// only explicitly allowed (client identity, RPC method) pairs are permitted.
func RPCPolicyMiddleware(policy RPCPolicy) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			// Extract RPC method from transport metadata
			tr, ok := transport.FromServerContext(ctx)
			if !ok {
				return nil, status.Error(codes.Internal, "transport context unavailable")
			}
			method := tr.Operation()

			// Extract client identity from mTLS certificate
			clientIdentity, err := extractClientIdentity(ctx)
			if err != nil {
				return nil, status.Errorf(codes.Unauthenticated, "client identity unavailable: %v", err)
			}

			// Check policy
			if !policy.IsAllowed(clientIdentity, method) {
				return nil, status.Errorf(codes.PermissionDenied,
					"client %q not authorized for method %q", clientIdentity, method)
			}

			return next(ctx, req)
		}
	}
}

// extractClientIdentity retrieves the DNS SAN from the client's mTLS certificate.
func extractClientIdentity(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer info")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("not a TLS connection")
	}

	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("no client certificate")
	}
	if tlsInfo.State.Version < tls.VersionTLS13 {
		return "", fmt.Errorf("client TLS version must be 1.3 or newer")
	}
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return "", fmt.Errorf("client certificate is not verified")
	}
	return clientDNSIdentity(tlsInfo.State.PeerCertificates[0])
}

// RPCPolicy defines which client identities can call which RPC methods.
type RPCPolicy interface {
	IsAllowed(clientIdentity, method string) bool
}

// StaticRPCPolicy is a simple map-based policy implementation.
type StaticRPCPolicy map[string][]string

func (p StaticRPCPolicy) IsAllowed(clientIdentity, method string) bool {
	allowedMethods, ok := p[clientIdentity]
	if !ok {
		return false
	}
	for _, m := range allowedMethods {
		if m == method {
			return true
		}
	}
	return false
}

// InferenceServicePolicy defines the RPC authorization policy for Inference Service.
func InferenceServicePolicy() RPCPolicy {
	policy := StaticRPCPolicy{
		// Governance Service can create and delete inference services (managed GPU commands)
		governanceCallerDNS: {
			"/inference.v1.InferenceServiceManager/CreateInferenceService",
			"/inference.v1.InferenceServiceManager/GetInferenceService",
			"/inference.v1.InferenceServiceManager/ListInferenceServices",
			"/inference.v1.InferenceServiceManager/DeleteInferenceService",
			"/inference.v1.InferenceServiceManager/UpdateInferenceService",
			"/inference.v1.InferenceServiceManager/StartInferenceService",
			"/inference.v1.InferenceServiceManager/StopInferenceService",
			"/inference.v1.InferenceServiceManager/RestartInferenceService",
			"/inference.v1.InferenceServiceManager/GetOperation",
			"/inference.v1.InferenceServiceManager/ListOperations",
			"/inference.v1.InferenceDiagnostics/GetRuntimeDiagnostics",
			"/inference.v1.InferenceDiagnostics/GetRuntimeLogs",
			"/inference.v1.InferenceDiagnostics/StreamRuntimeLogs",
			"/inference.v1.InferenceDiagnostics/ListRuntimeEvents",
		},
		// Model Service can check if model versions are still in use
		"ani-model.ani.internal": {
			"/inference.v1.ModelReferenceService/CheckModelVersionReferences",
		},
		// Add other trusted services here as needed
	}

	// Wrap with explicit identity validation
	return &ValidatedPolicy{
		underlying: policy,
		expectedIdentities: map[string]string{
			governanceCallerDNS:      "Governance Service",
			"ani-model.ani.internal": "Model Service",
		},
	}
}

// ValidatedPolicy wraps a policy with explicit service identity validation.
type ValidatedPolicy struct {
	underlying         RPCPolicy
	expectedIdentities map[string]string // DNS SAN -> service name (for logging)
}

func (v *ValidatedPolicy) IsAllowed(clientIdentity, method string) bool {
	// First, verify the client identity is in our expected list
	if _, expected := v.expectedIdentities[clientIdentity]; !expected {
		// Unknown client identity - deny by default
		return false
	}

	// Then check the underlying policy
	return v.underlying.IsAllowed(clientIdentity, method)
}
