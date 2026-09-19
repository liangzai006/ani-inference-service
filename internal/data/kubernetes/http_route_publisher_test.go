package kubernetes

import (
	"context"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type publicationRuntimeSource struct {
	desired DesiredRuntime
}

func (s publicationRuntimeSource) CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error) {
	return s.desired, nil
}

func testPublisher(objects ...client.Object) *HTTPRoutePublisher {
	return &HTTPRoutePublisher{
		Client: fake.NewClientBuilder().WithObjects(objects...).Build(),
		Source: publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
			TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models",
			Endpoint: &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
		}}},
		GatewayNamespace: "ingress-apisix", GatewayName: "ani-apisix",
		RouteNamespace: "models", HostSuffix: "vllm.test", PathPrefix: "/v1",
	}
}

func testPublication() publication.Publication {
	return publication.Publication{TenantID: "tenant", ServiceID: "service", Generation: 3}
}

func TestHTTPRoutePublisherPublishAndEndpoint(t *testing.T) {
	ctx := context.Background()
	p := testPublisher()
	pub := testPublication()
	if err := p.Publish(ctx, pub); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	got := &unstructured.Unstructured{}
	got.SetAPIVersion(httpRouteAPIVersion)
	got.SetKind(httpRouteKind)
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: "models", Name: routeName(pub)}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.GetLabels()[serviceIDLabel] != "service" {
		t.Fatalf("route labels = %#v", got.GetLabels())
	}
	rules, found, err := unstructured.NestedSlice(got.Object, "spec", "rules")
	if err != nil || !found || len(rules) != 1 {
		t.Fatalf("rules = %#v found=%v err=%v", rules, found, err)
	}
	rule, ok := rules[0].(map[string]interface{})
	if !ok {
		t.Fatalf("rule = %#v", rules[0])
	}
	backend, found, err := unstructured.NestedSlice(rule, "backendRefs")
	if err != nil || !found || len(backend) != 1 {
		t.Fatalf("backendRefs = %#v found=%v err=%v", backend, found, err)
	}
	endpoint, err := p.Endpoint(ctx, pub)
	if err != nil || endpoint != "http://service.vllm.test/v1" {
		t.Fatalf("Endpoint() = %q, %v", endpoint, err)
	}
}

func TestHTTPRoutePublisherConfirmPublishedRequiresBothConditions(t *testing.T) {
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "model-endpoint", 80)
	_ = unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
		"parentRef": map[string]interface{}{"name": "ani-apisix", "namespace": "ingress-apisix"},
		"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": "True"},
		},
	}}, "status", "parents")
	if err := p.Client.Create(context.Background(), route); err != nil {
		t.Fatalf("create route: %v", err)
	}
	confirmed, err := p.ConfirmPublished(context.Background(), pub)
	if err != nil || confirmed {
		t.Fatalf("ConfirmPublished() = %v, %v; want false without ResolvedRefs", confirmed, err)
	}
	_ = unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
		"parentRef": map[string]interface{}{"name": "ani-apisix", "namespace": "ingress-apisix"},
		"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": "True"},
			map[string]interface{}{"type": "ResolvedRefs", "status": "True"},
		},
	}}, "status", "parents")
	if err := p.Client.Update(context.Background(), route); err != nil {
		t.Fatalf("update route status: %v", err)
	}
	confirmed, err = p.ConfirmPublished(context.Background(), pub)
	if err != nil || !confirmed {
		t.Fatalf("ConfirmPublished() = %v, %v; want true", confirmed, err)
	}
}

func TestHTTPRoutePublisherWithdrawIsFencedAndConvergent(t *testing.T) {
	ctx := context.Background()
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "model-endpoint", 80)
	route.SetUID("route-uid")
	if err := p.Client.Create(ctx, route); err != nil {
		t.Fatalf("create route: %v", err)
	}
	if err := p.Withdraw(ctx, pub); err != nil {
		t.Fatalf("Withdraw() error = %v", err)
	}
	confirmed, err := p.ConfirmWithdrawn(ctx, pub)
	if err != nil || !confirmed {
		t.Fatalf("ConfirmWithdrawn() = %v, %v; want true", confirmed, err)
	}
	if err := p.Withdraw(ctx, pub); err != nil {
		t.Fatalf("second Withdraw() error = %v", err)
	}
}
