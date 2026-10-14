package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/server"
	"github.com/liangzai006/ani-inference-service/internal/service"
	integrationv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// These certificates and loopback listeners exercise the production TLS,
// identity, RPC policy and service validation, without persistent resources.
type receiverTestPKI struct {
	directory string
	ca        *x509.Certificate
	key       ed25519.PrivateKey
	caFile    string
	serial    int64
}

func newReceiverTestPKI(t *testing.T) *receiverTestPKI {
	t.Helper()
	p := &receiverTestPKI{directory: t.TempDir()}
	_, _, p.caFile, _ = p.certificate(t, "ca", &x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign})
	return p
}

func (p *receiverTestPKI) certificate(t *testing.T, name string, spec *x509.Certificate) (tls.Certificate, *x509.Certificate, string, string) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p.serial++
	spec.SerialNumber = big.NewInt(p.serial)
	spec.Subject = pkix.Name{CommonName: name}
	spec.NotBefore, spec.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	spec.KeyUsage |= x509.KeyUsageDigitalSignature
	parent, parentKey := p.ca, p.key
	if parent == nil {
		parent, parentKey = spec, key
	}
	der, err := x509.CreateCertificate(rand.Reader, spec, parent, public, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if p.ca == nil {
		p.ca, p.key = leaf, key
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(p.directory, name+".crt"), filepath.Join(p.directory, name+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return pair, leaf, certFile, keyFile
}

func startReceiverTestServer(t *testing.T, srv *kratosgrpc.Server) string {
	t.Helper()
	endpoint, err := srv.Endpoint() // binds the production listener to loopback :0
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return endpoint.Host
}

func newGovernanceIdentityReceivers(t *testing.T) (*receiverTestPKI, map[string]string) {
	t.Helper()
	pki := newReceiverTestPKI(t)
	_, _, serverCert, serverKey := pki.certificate(t, "server", &x509.Certificate{DNSNames: []string{"ani-inference.ani.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	t.Setenv("ANI_SERVER_TLS_CA_FILE", pki.caFile)
	t.Setenv("ANI_SERVER_TLS_CERT_FILE", serverCert)
	t.Setenv("ANI_SERVER_TLS_KEY_FILE", serverKey)
	config := &inferencev1.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(time.Second)}
	owner := service.NewInferenceServerWithAll(nil, nil, nil, nil)
	policy := RPCPolicyMiddleware(InferenceServicePolicy())
	ordinary, err := configuredInferenceGRPCServer(config, policy, server.DirectTenantMiddleware())
	if err != nil {
		t.Fatal(err)
	}
	inferencev1.RegisterInferenceServiceManagerServer(ordinary, owner)
	managed := validManagedConfig()
	managed.Receiver.Address = "127.0.0.1:0"
	managed.Receiver.CaFile, managed.Receiver.CertFile, managed.Receiver.KeyFile = pki.caFile, serverCert, serverKey
	managedServer, err := configuredGovernanceGRPCServer(config, managed, owner, policy, server.DirectTenantMiddleware())
	if err != nil {
		t.Fatal(err)
	}
	return pki, map[string]string{"ordinary": startReceiverTestServer(t, ordinary), "managed": startReceiverTestServer(t, managedServer)}
}

func TestGovernanceDNSIdentityThroughProductionReceivers(t *testing.T) {
	pki, addresses := newGovernanceIdentityReceivers(t)
	roots := x509.NewCertPool()
	roots.AddCert(pki.ca)
	uri, err := url.Parse("spiffe://ani.internal/service/ani-governance")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		spec x509.Certificate
		code codes.Code
	}{
		{name: "registered-dns", spec: x509.Certificate{DNSNames: []string{"ani-governance"}}, code: codes.InvalidArgument},
		{name: "retired-dns", spec: x509.Certificate{DNSNames: []string{"ani-governance.ani.internal"}}, code: codes.PermissionDenied},
		{name: "unregistered-dns", spec: x509.Certificate{DNSNames: []string{"other-service"}}, code: codes.PermissionDenied},
		{name: "uri-only", spec: x509.Certificate{URIs: []*url.URL{uri}}, code: codes.Unavailable},
		{name: "mixed-uri", spec: x509.Certificate{DNSNames: []string{"ani-governance"}, URIs: []*url.URL{uri}}, code: codes.Unavailable},
		{name: "mixed-ip", spec: x509.Certificate{DNSNames: []string{"ani-governance"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}, code: codes.Unavailable},
		{name: "mixed-email", spec: x509.Certificate{DNSNames: []string{"ani-governance"}, EmailAddresses: []string{"governance@example.invalid"}}, code: codes.Unavailable},
		{name: "multiple-dns", spec: x509.Certificate{DNSNames: []string{"ani-governance", "other-service"}}, code: codes.Unavailable},
	}
	const tenant = "10000000-0000-4000-8000-000000000001"
	for _, test := range cases {
		test.spec.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		pair, _, _, _ := pki.certificate(t, test.name, &test.spec)
		for receiver, address := range addresses {
			t.Run(receiver+"/"+test.name, func(t *testing.T) {
				conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "ani-inference.ani.internal", Certificates: []tls.Certificate{pair}})))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", "governance:user:7"))
				rpc := inferencev1.NewInferenceServiceManagerClient(conn)
				create := &inferencev1.CreateInferenceServiceRequest{}
				if receiver == "managed" {
					create.GpuOwnerAttachment = &integrationv1.GpuOwnerCreateAttachment{Ref: &acceleratorv1.GpuUsageRef{TenantId: tenant}}
				}
				_, err = rpc.CreateInferenceService(ctx, create)
				if status.Code(err) != test.code {
					t.Fatalf("CREATE code=%v err=%v want=%v", status.Code(err), err, test.code)
				}
				if test.name != "registered-dns" {
					return
				}
				if !strings.Contains(status.Convert(err).Message(), "request_id, name and model_version_id are required") {
					t.Fatalf("CREATE did not reach production validation: %v", err)
				}
				_, err = rpc.GetInferenceService(ctx, &inferencev1.GetInferenceServiceRequest{})
				if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "resource_id is required") {
					t.Fatalf("GET did not reach production validation: %v", err)
				}
				_, err = rpc.UpdateInferenceService(ctx, &inferencev1.UpdateInferenceServiceRequest{})
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("method outside RPC policy accepted: %v", err)
				}
				if receiver == "managed" {
					conflicting := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", "10000000-0000-4000-8000-000000000099", "x-ani-actor", "governance:user:7"))
					_, err = rpc.CreateInferenceService(conflicting, create)
					if status.Code(err) != codes.PermissionDenied {
						t.Fatalf("attachment tenant conflict accepted: %v", err)
					}
				}
			})
		}
	}
}

// TestGovernanceIdentityInteropProcess exposes only the production RPC surface
// for an explicitly opted-in cross-repository client certificate check. Empty
// business requests reach production validation; no persistence is configured.
func TestGovernanceIdentityInteropProcess(t *testing.T) {
	directory := os.Getenv("INFERENCE_IDENTITY_INTEROP_DIR")
	if directory == "" {
		t.Skip("set INFERENCE_IDENTITY_INTEROP_DIR for cross-repository TLS interoperability")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		t.Fatalf("INFERENCE_IDENTITY_INTEROP_DIR must be an existing task-owned directory: %v", err)
	}
	pki, addresses := newGovernanceIdentityReceivers(t)
	_, _, clientCert, clientKey := pki.certificate(t, "governance-client", &x509.Certificate{DNSNames: []string{"ani-governance"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	encoded, err := json.Marshal(map[string]string{
		"ordinary_endpoint": addresses["ordinary"], "managed_endpoint": addresses["managed"],
		"ca_file": pki.caFile, "client_cert_file": clientCert, "client_key_file": clientKey,
		"server_name": "ani-inference.ani.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "endpoints.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	stopped, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-stopped.Done()
	if ctx.Err() != nil {
		t.Fatalf("cross-repository client did not stop the receiver within 60 seconds: %v", ctx.Err())
	}
}
