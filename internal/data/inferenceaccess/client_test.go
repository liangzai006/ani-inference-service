package inferenceaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/service/gatewayauthz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type accessKeyClientStub struct {
	inferencev1.InferenceAccessKeyServiceClient
	request *inferencev1.CheckInferenceAccessRequest
}

func (s *accessKeyClientStub) CheckInferenceAccess(_ context.Context, req *inferencev1.CheckInferenceAccessRequest, _ ...grpc.CallOption) (*inferencev1.CheckInferenceAccessResponse, error) {
	s.request = req
	return &inferencev1.CheckInferenceAccessResponse{Allowed: true, TenantId: "tenant-a", KeyId: "key-a", PolicyVersion: 4}, nil
}

func TestNewClientDelegatesCheck(t *testing.T) {
	stub := &accessKeyClientStub{}
	client, err := NewClient(stub, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Check(context.Background(), gatewayauthz.CheckRequest{APIKey: "iak_secret", ServiceID: "service-a", ServedModelName: "model-a", RequestID: "request-a", RequestCost: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Allowed || result.TenantID != "tenant-a" || result.InferenceKeyID != "key-a" || result.PolicyVersion != 4 {
		t.Fatalf("result = %+v", result)
	}
	if stub.request.GetApiKey() != "iak_secret" || stub.request.GetServiceId() != "service-a" || stub.request.GetServedModelName() != "model-a" || stub.request.GetRequestId() != "request-a" {
		t.Fatalf("request = %+v", stub.request)
	}
}

func TestConfigValidateRequiresMutualTLSIdentity(t *testing.T) {
	cfg := Config{Endpoint: "inference:19090", Timeout: time.Second}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a client without CA, certificate, key, and identities")
	}
	cfg.TLS = TLSConfig{CAFile: "ca.crt", CertFile: "tls.crt", KeyFile: "tls.key", ServerName: "ani-inference.ani.internal", ClientIdentity: "ani-inference-gateway.ani.internal"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected complete config: %v", err)
	}
}

func TestDialWaitsForDelayedMutualTLSWithoutChangingRequestTimeout(t *testing.T) {
	directory := t.TempDir()
	makeCert := func(name string, serial int64, ca *x509.Certificate, caKey ed25519.PrivateKey, usage x509.ExtKeyUsage) (tls.Certificate, *x509.Certificate, ed25519.PrivateKey, string, string) {
		t.Helper()
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, DNSNames: []string{name}}
		if ca == nil {
			tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
			tmpl.KeyUsage |= x509.KeyUsageCertSign
			ca, caKey = tmpl, key
		} else {
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{usage}
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certFile, keyFile := filepath.Join(directory, name+".crt"), filepath.Join(directory, name+".key")
		if err = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		return pair, parsed, key, certFile, keyFile
	}
	_, ca, caKey, caFile, _ := makeCert("test-ca", 1, nil, nil, 0)
	server, _, _, _, _ := makeCert("inference.test", 2, ca, caKey, x509.ExtKeyUsageServerAuth)
	_, _, _, clientCert, clientKey := makeCert("authz.test", 3, ca, caKey, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})))
	timer := time.AfterFunc(250*time.Millisecond, func() { _ = srv.Serve(listener) })
	t.Cleanup(func() { timer.Stop(); srv.Stop(); _ = listener.Close() })
	cfg := Config{Endpoint: listener.Addr().String(), Timeout: 50 * time.Millisecond, DialTimeout: 4 * time.Second, TLS: TLSConfig{CAFile: caFile, CertFile: clientCert, KeyFile: clientKey, ServerName: "inference.test", ClientIdentity: "authz.test"}}
	client, closeClient, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("delayed server connection failed: %v", err)
	}
	defer closeClient()
	if client.timeout != cfg.Timeout {
		t.Fatalf("request timeout = %s, want %s", client.timeout, cfg.Timeout)
	}
}
