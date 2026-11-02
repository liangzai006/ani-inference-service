package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	imagedata "github.com/liangzai006/ani-inference-service/internal/data/image"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func configuredImageClient() (*imagedata.Client, func(), error) {
	address := strings.TrimSpace(os.Getenv("ANI_IMAGE_RUNTIME_GRPC_ADDR"))
	if address == "" {
		return nil, func() {}, nil
	}
	values := map[string]string{}
	for _, key := range []string{"ANI_IMAGE_RUNTIME_CA_FILE", "ANI_IMAGE_RUNTIME_CERT_FILE", "ANI_IMAGE_RUNTIME_KEY_FILE", "ANI_IMAGE_TARGET_OS", "ANI_IMAGE_TARGET_ARCH", "ANI_INFERENCE_NAMESPACE", "ANI_INFERENCE_RUNTIME_CLUSTER_ID"} {
		values[key] = strings.TrimSpace(os.Getenv(key))
		if values[key] == "" {
			return nil, func() {}, fmt.Errorf("%s is required for catalog images", key)
		}
	}
	ca, err := os.ReadFile(values["ANI_IMAGE_RUNTIME_CA_FILE"])
	if err != nil {
		return nil, func() {}, errors.New("read Resource image runtime CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, func() {}, errors.New("invalid Resource image runtime CA")
	}
	cert, err := tls.LoadX509KeyPair(values["ANI_IMAGE_RUNTIME_CERT_FILE"], values["ANI_IMAGE_RUNTIME_KEY_FILE"])
	if err != nil {
		return nil, func() {}, errors.New("load Inference image runtime identity")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "ani-inference-service" {
		return nil, func() {}, errors.New("image runtime client requires ani-inference-service DNS identity")
	}
	serverName := "ani-network-service"
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: serverName, VerifyConnection: func(s tls.ConnectionState) error {
		if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 {
			return errors.New("unverified Resource runtime")
		}
		for _, name := range s.PeerCertificates[0].DNSNames {
			if name == serverName {
				return nil
			}
		}
		return errors.New("Resource runtime DNS identity mismatch")
	}})))
	if err != nil {
		return nil, func() {}, err
	}
	return &imagedata.Client{Runtime: imagev1.NewImageRuntimeServiceClient(conn), Platform: &imagev1.ImagePlatform{Os: values["ANI_IMAGE_TARGET_OS"], Architecture: values["ANI_IMAGE_TARGET_ARCH"], Variant: strings.TrimSpace(os.Getenv("ANI_IMAGE_TARGET_VARIANT"))}, Namespace: values["ANI_INFERENCE_NAMESPACE"], ClusterID: values["ANI_INFERENCE_RUNTIME_CLUSTER_ID"], RequireCatalog: true}, func() { _ = conn.Close() }, nil
}
