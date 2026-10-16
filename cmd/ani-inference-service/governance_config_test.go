package main

import (
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

func validManagedConfig() *inferencev1.ManagedGPU {
	return &inferencev1.ManagedGPU{Refund: &inferencev1.ManagedGPU_Refund{Address: "quota-address", CaFile: "quota-ca", CertFile: "refund-cert", KeyFile: "refund-key", ServerName: "quota-service", Timeout: durationpb.New(3 * time.Second)}}
}

func TestManagedGPUConfigIsTypedCompleteAndOptional(t *testing.T) {
	if _, enabled, err := governanceRefundConfig(nil); err != nil || enabled {
		t.Fatalf("CPU configuration: enabled=%v err=%v", enabled, err)
	}
	if _, enabled, err := governanceRefundConfig(&inferencev1.ManagedGPU{Refund: &inferencev1.ManagedGPU_Refund{Timeout: durationpb.New(5 * time.Second)}}); err != nil || enabled {
		t.Fatalf("empty YAML config: enabled=%v err=%v", enabled, err)
	}
	cfg, enabled, err := governanceRefundConfig(validManagedConfig())
	if err != nil || !enabled || cfg.Timeout != 3*time.Second || cfg.TLS.ServerName != "quota-service" {
		t.Fatalf("managed config=%v enabled=%v err=%v", cfg, enabled, err)
	}
	for _, test := range []struct {
		name  string
		alter func(*inferencev1.ManagedGPU)
	}{
		{"missing-address", func(c *inferencev1.ManagedGPU) { c.Refund.Address = "" }},
		{"missing-client-ca", func(c *inferencev1.ManagedGPU) { c.Refund.CaFile = "" }},
		{"missing-client-cert", func(c *inferencev1.ManagedGPU) { c.Refund.CertFile = "" }},
		{"missing-client-key", func(c *inferencev1.ManagedGPU) { c.Refund.KeyFile = "" }},
		{"missing-server-name", func(c *inferencev1.ManagedGPU) { c.Refund.ServerName = "" }},
		{"negative-timeout", func(c *inferencev1.ManagedGPU) { c.Refund.Timeout = durationpb.New(-time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := validManagedConfig()
			test.alter(c)
			if _, _, err := governanceRefundConfig(c); err == nil {
				t.Fatal("incomplete managed config accepted")
			}
		})
	}
}
