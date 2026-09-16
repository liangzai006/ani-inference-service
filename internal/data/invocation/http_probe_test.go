package invocation

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

func TestHTTPProbeClassifiesEndpointHealth(t *testing.T) {
	for name, status := range map[string]int{"healthy": http.StatusOK, "unhealthy": http.StatusServiceUnavailable} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Skipf("loopback listener unavailable: %v", err)
			}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			server.Listener = listener
			server.Start()
			defer server.Close()
			probe := HTTPProbe{Resolver: func(kubernetes.RuntimeSpec) (string, error) { return server.URL, nil }}
			got, err := probe.Probe(context.Background(), kubernetes.RuntimeSpec{})
			if err != nil || !got.Known || got.Healthy != (status == http.StatusOK) {
				t.Fatalf("probe=%+v err=%v", got, err)
			}
		})
	}
}

func TestHTTPProbeTransportFailureIsUnknown(t *testing.T) {
	probe := HTTPProbe{Resolver: func(kubernetes.RuntimeSpec) (string, error) { return "http://127.0.0.1:1", nil }, Client: &http.Client{}}
	got, err := probe.Probe(context.Background(), kubernetes.RuntimeSpec{})
	if err != nil || got.Known || got.Healthy || got.Reason == "" {
		t.Fatalf("probe=%+v err=%v, want unknown transport result", got, err)
	}
}

func TestHTTPProbeRequiresExplicitResolver(t *testing.T) {
	if _, err := (HTTPProbe{}).Probe(context.Background(), kubernetes.RuntimeSpec{}); err == nil {
		t.Fatal("probe accepted missing endpoint resolver")
	}
}
