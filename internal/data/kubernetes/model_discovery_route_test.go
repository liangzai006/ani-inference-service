package kubernetes

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"testing"
)

func TestModelDiscoveryRouteRequiresNoModelHeaderAndKeepsCredentialsAtAuthAdapter(t *testing.T) {
	p := testPublisher()
	if err := p.EnsureModelDiscoveryRoute(context.Background(), "models", "custom-authz", 8181); err != nil {
		t.Fatal(err)
	}
	route := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "models", Name: "ani-inference-models"}, route); err != nil {
		t.Fatal(err)
	}
	rule := route.Spec.Rules[0]
	match := rule.Matches[0]
	if len(match.Headers) != 0 || match.Path == nil || *match.Path.Type != gatewayv1.PathMatchExact || *match.Path.Value != "/v1/models" || match.Method == nil || *match.Method != "GET" {
		t.Fatalf("discovery match=%+v", match)
	}
	backend := rule.BackendRefs[0]
	if backend.Name != "custom-authz" || backend.Port == nil || *backend.Port != 8181 || len(rule.Filters) != 0 {
		t.Fatalf("discovery must go to credential authority: %+v", rule)
	}
	if err := p.EnsureModelDiscoveryRoute(context.Background(), "models", "custom-authz", 8181); err != nil {
		t.Fatalf("repeat reconcile: %v", err)
	}
}
