package governance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	quotav1 "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type releaseStub struct {
	requests []*quotav1.ReportResourceReleasedRequest
	reply    *quotav1.ReportResourceReleasedResponse
	err      error
	wait     bool
}

func (s *releaseStub) ReportQuotaRelease(ctx context.Context, req *quotav1.ReportQuotaReleaseRequest, _ ...grpc.CallOption) (*quotav1.ReportQuotaReleaseResponse, error) {
	if s.wait {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return nil, s.err
}

func (s *releaseStub) ReportResourceReleased(ctx context.Context, req *quotav1.ReportResourceReleasedRequest, _ ...grpc.CallOption) (*quotav1.ReportResourceReleasedResponse, error) {
	s.requests = append(s.requests, proto.Clone(req).(*quotav1.ReportResourceReleasedRequest))
	if s.wait {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return s.reply, s.err
}

func releaseNotification() gpu.ReleaseNotification {
	return gpu.ReleaseNotification{ReleaseEventID: "10000000-0000-4000-8000-000000000001", CreateOperationID: "10000000-0000-4000-8000-000000000002", ResourceID: "10000000-0000-4000-8000-000000000003", Reason: gpu.ResourceReleased}
}

func TestClientReportsOnlyReleaseIdentityAndStableRetry(t *testing.T) {
	n := releaseNotification()
	rpc := &releaseStub{reply: &quotav1.ReportResourceReleasedResponse{ReleaseEventId: n.ReleaseEventID, CreateOperationId: n.CreateOperationID, ResourceId: n.ResourceID, Status: quotav1.ResourceReleaseStatus_APPLIED}}
	client, err := NewClient(rpc, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReportResourceReleased(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := client.ReportResourceReleased(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(rpc.requests) != 2 || !proto.Equal(rpc.requests[0], rpc.requests[1]) || rpc.requests[0].CreateOperationId != n.CreateOperationID || rpc.requests[0].ReleaseEventId != n.ReleaseEventID || rpc.requests[0].ResourceId != n.ResourceID {
		t.Fatalf("retry changed release identity: %v", rpc.requests)
	}
	bad := n
	bad.ResourceID = ""
	if err := client.ReportResourceReleased(context.Background(), bad); !errors.Is(err, gpu.ErrInvalidRefund) || len(rpc.requests) != 2 {
		t.Fatalf("invalid identity reached Governance: %v", err)
	}
}

func TestClientPropagatesRPCAndTimeoutErrors(t *testing.T) {
	n := releaseNotification()
	rpc := &releaseStub{err: status.Error(codes.Unavailable, "receiver unavailable")}
	client, _ := NewClient(rpc, nil, time.Second)
	if err := client.ReportResourceReleased(context.Background(), n); status.Code(err) != codes.Unavailable {
		t.Fatalf("RPC error lost: %v", err)
	}
	rpc.err = nil
	rpc.reply = &quotav1.ReportResourceReleasedResponse{}
	rpc.wait = true
	client.timeout = 10 * time.Millisecond
	if err := client.ReportResourceReleased(context.Background(), n); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("fixed timeout not enforced: %v", err)
	}
}

func TestClientValidatesResourceReleaseResponse(t *testing.T) {
	n := releaseNotification()
	responses := map[string]*quotav1.ReportResourceReleasedResponse{
		"nil":                 nil,
		"mismatched event":    {ReleaseEventId: "10000000-0000-4000-8000-000000000099", CreateOperationId: n.CreateOperationID, ResourceId: n.ResourceID, Status: quotav1.ResourceReleaseStatus_APPLIED},
		"mismatched create":   {ReleaseEventId: n.ReleaseEventID, CreateOperationId: "10000000-0000-4000-8000-000000000099", ResourceId: n.ResourceID, Status: quotav1.ResourceReleaseStatus_APPLIED},
		"mismatched resource": {ReleaseEventId: n.ReleaseEventID, CreateOperationId: n.CreateOperationID, ResourceId: "10000000-0000-4000-8000-000000000099", Status: quotav1.ResourceReleaseStatus_APPLIED},
		"unspecified":         {ReleaseEventId: n.ReleaseEventID, CreateOperationId: n.CreateOperationID, ResourceId: n.ResourceID},
	}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			client, err := NewClient(&releaseStub{reply: response}, nil, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.ReportResourceReleased(context.Background(), n); !errors.Is(err, gpu.ErrInvalidRefund) {
				t.Fatalf("response accepted: %v", err)
			}
		})
	}
	for _, status := range []quotav1.ResourceReleaseStatus{quotav1.ResourceReleaseStatus_APPLIED, quotav1.ResourceReleaseStatus_ALREADY_APPLIED} {
		client, err := NewClient(&releaseStub{reply: &quotav1.ReportResourceReleasedResponse{ReleaseEventId: n.ReleaseEventID, CreateOperationId: n.CreateOperationID, ResourceId: n.ResourceID, Status: status}}, nil, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.ReportResourceReleased(context.Background(), n); err != nil {
			t.Fatalf("status %v rejected: %v", status, err)
		}
	}
}

type mtlsReleaseServer struct {
	quotav1.UnimplementedQuotaReleaseServiceServer
	mu       sync.Mutex
	requests []*quotav1.ReportResourceReleasedRequest
}

func (s *mtlsReleaseServer) ReportQuotaRelease(ctx context.Context, req *quotav1.ReportQuotaReleaseRequest) (*quotav1.ReportQuotaReleaseResponse, error) {
	if err := s.checkTLS(ctx); err != nil {
		return nil, err
	}
	return &quotav1.ReportQuotaReleaseResponse{}, nil
}

func (s *mtlsReleaseServer) ReportResourceReleased(ctx context.Context, req *quotav1.ReportResourceReleasedRequest) (*quotav1.ReportResourceReleasedResponse, error) {
	if err := s.checkTLS(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, proto.Clone(req).(*quotav1.ReportResourceReleasedRequest))
	return &quotav1.ReportResourceReleasedResponse{ReleaseEventId: req.ReleaseEventId, CreateOperationId: req.CreateOperationId, ResourceId: req.ResourceId, Status: quotav1.ResourceReleaseStatus_APPLIED}, nil
}

func (s *mtlsReleaseServer) checkTLS(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing TLS")
	}
	info, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || info.State.Version != tls.VersionTLS13 || len(info.State.VerifiedChains) == 0 {
		return status.Error(codes.Unauthenticated, "verified TLS1.3 required")
	}
	if names := info.State.PeerCertificates[0].DNSNames; len(names) != 1 || names[0] != "inference.refund.test.invalid" {
		return status.Error(codes.PermissionDenied, "wrong owner DNS SAN")
	}
	return nil
}

func testCertificate(t *testing.T, directory, name string, serial int64, ca *x509.Certificate, caKey ed25519.PrivateKey, server bool) (tls.Certificate, *x509.Certificate, ed25519.PrivateKey, string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, DNSNames: []string{name}}
	if ca == nil {
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage |= x509.KeyUsageCertSign
		ca = template
		caKey = key
	} else if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(directory, name+".pem"), filepath.Join(directory, name+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return pair, parsed, key, certFile, keyFile
}

func TestDialUsesRealMutualTLSAndRejectsWrongCANameAndOwner(t *testing.T) {
	directory := t.TempDir()
	_, ca, caKey, caFile, _ := testCertificate(t, directory, "ca", 1, nil, nil, false)
	serverCert, _, _, _, _ := testCertificate(t, directory, "quota.refund.test.invalid", 2, ca, caKey, true)
	_, _, _, clientCert, clientKey := testCertificate(t, directory, "inference.refund.test.invalid", 3, ca, caKey, false)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := &mtlsReleaseServer{}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})))
	quotav1.RegisterQuotaReleaseServiceServer(srv, rpc)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { srv.Stop(); _ = listener.Close() })
	n := releaseNotification()
	cfg := Config{Endpoint: listener.Addr().String(), Timeout: time.Second, TLS: TLSConfig{CAFile: caFile, CertFile: clientCert, KeyFile: clientKey, ServerName: "quota.refund.test.invalid"}}
	client, closeClient, err := Dial(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeClient() })
	if err := client.ReportResourceReleased(context.Background(), n); err != nil {
		t.Fatalf("mTLS release report: %v", err)
	}
	bad := cfg
	bad.Timeout = 50 * time.Millisecond
	bad.TLS.ServerName = "wrong.refund.test.invalid"
	if c, closeBad, err := Dial(context.Background(), bad, nil); err == nil {
		_ = closeBad()
		_ = c
		t.Fatal("wrong server name accepted")
	}
	_, _, _, otherCA, _ := testCertificate(t, directory, "other-ca", 4, nil, nil, false)
	bad = cfg
	bad.Timeout = 50 * time.Millisecond
	bad.TLS.CAFile = otherCA
	if _, closeBad, err := Dial(context.Background(), bad, nil); err == nil {
		_ = closeBad()
		t.Fatal("wrong CA accepted")
	}
	_, _, _, wrongCert, wrongKey := testCertificate(t, directory, "wrong-owner.refund.test.invalid", 5, ca, caKey, false)
	bad = cfg
	bad.TLS.CertFile = wrongCert
	bad.TLS.KeyFile = wrongKey
	wrong, closeWrong, err := Dial(context.Background(), bad, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWrong()
	if err := wrong.ReportResourceReleased(context.Background(), n); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong owner accepted: %v", err)
	}
}
