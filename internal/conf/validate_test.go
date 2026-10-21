package conf

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestBootstrapValidation(t *testing.T) {
	valid := &Bootstrap{Server: &Server{
		Grpc:            &Server_GRPC{Network: "tcp", Addr: "127.0.0.1:19090", Timeout: durationpb.New(time.Second)},
		Admin:           &Server_Admin{Network: "tcp", Addr: "127.0.0.1:19091", Timeout: durationpb.New(time.Second)},
		ShutdownTimeout: durationpb.New(5 * time.Second),
	}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Bootstrap)
	}{
		{"missing-server", func(c *Bootstrap) { c.Server = nil }},
		{"missing-admin", func(c *Bootstrap) { c.Server.Admin = nil }},
		{"duplicate-port", func(c *Bootstrap) { c.Server.Admin.Addr = "0.0.0.0:19090" }},
		{"invalid-address", func(c *Bootstrap) { c.Server.Grpc.Addr = "localhost:19090" }},
		{"invalid-network", func(c *Bootstrap) { c.Server.Grpc.Network = "udp" }},
		{"missing-timeout", func(c *Bootstrap) { c.Server.Grpc.Timeout = nil }},
		{"negative-timeout", func(c *Bootstrap) { c.Server.Grpc.Timeout = durationpb.New(-time.Second) }},
		{"long-shutdown", func(c *Bootstrap) { c.Server.ShutdownTimeout = durationpb.New(31 * time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := proto.Clone(valid).(*Bootstrap)
			tc.mutate(candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	var missing *Bootstrap
	if err := missing.Validate(); err == nil {
		t.Fatal("nil configuration accepted")
	}
}
