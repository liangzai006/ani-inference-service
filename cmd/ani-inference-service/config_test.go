package main

import (
	"testing"
	"time"

	"github.com/go-kratos/kratos/v3/config"
	"github.com/go-kratos/kratos/v3/config/env"
	"github.com/go-kratos/kratos/v3/config/file"
	"github.com/liangzai006/ani-inference-service/internal/conf"
)

func TestInternalConfigLoadsExistingYAML(t *testing.T) {
	for key, value := range map[string]string{
		"SERVER_GRPC_NETWORK": "tcp", "SERVER_GRPC_ADDR": "127.0.0.1:29090", "SERVER_GRPC_TIMEOUT": "2s",
		"SERVER_ADMIN_NETWORK": "tcp", "SERVER_ADMIN_ADDR": "127.0.0.1:29091", "SERVER_ADMIN_TIMEOUT": "3s",
		"SERVER_SHUTDOWN_TIMEOUT":    "5s",
		"GOVERNANCE_QUOTA_GRPC_ADDR": "", "GOVERNANCE_QUOTA_CA_FILE": "", "GOVERNANCE_QUOTA_CERT_FILE": "",
		"GOVERNANCE_QUOTA_KEY_FILE": "", "GOVERNANCE_QUOTA_SERVER_NAME": "", "GOVERNANCE_QUOTA_TIMEOUT": "5s",
	} {
		// The production env source strips ANI_ before resolving placeholders.
		t.Setenv("ANI_"+key, value)
	}
	c := config.New(config.WithSource(file.NewSource("../../configs/config.yaml"), env.NewSource("ANI")))
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Load(); err != nil {
		t.Fatal(err)
	}
	var bootstrap conf.Bootstrap
	if err := c.Scan(&bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Validate(); err != nil {
		t.Fatal(err)
	}
	if bootstrap.Server.Grpc.Addr != "127.0.0.1:29090" || bootstrap.Server.Grpc.Timeout.AsDuration() != 2*time.Second || bootstrap.Server.Admin.Timeout.AsDuration() != 3*time.Second {
		t.Fatal("existing YAML fields or environment substitution changed")
	}
	if _, enabled, err := governanceRefundConfig(bootstrap.ManagedGpu); err != nil || enabled {
		t.Fatalf("empty refund configuration: enabled=%v error=%v", enabled, err)
	}
}
