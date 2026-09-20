package kubernetes

import (
	"context"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type publicationRuntimeSource struct {
	desired DesiredRuntime
}

func (s publicationRuntimeSource) CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error) {
	return s.desired, nil
}

func testPublisher(objects ...client.Object) *HTTPRoutePublisher {
	scheme := runtime.NewScheme()
	if err := gatewayv1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	return &HTTPRoutePublisher{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Source: publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
			TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models",
			Endpoint: &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
		}}},
		GatewayNamespace: "ingress-apisix", GatewayName: "ani-apisix",
		RouteNamespace: "models", PublicBaseURL: "http://10.10.1.67:30090", PathPrefix: "/v1",
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
	got := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: "models", Name: routeName(pub)}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	if got.GetLabels()[serviceIDLabel] != "service" {
		t.Fatalf("route labels = %#v", got.GetLabels())
	}
	if len(got.Spec.ParentRefs) != 1 || string(got.Spec.ParentRefs[0].Name) != "ani-apisix" {
		t.Fatalf("parentRefs = %#v", got.Spec.ParentRefs)
	}
	if len(got.Spec.Hostnames) != 0 {
		t.Fatalf("hostnames = %#v; want catch-all route", got.Spec.Hostnames)
	}
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].BackendRefs) != 1 || string(got.Spec.Rules[0].BackendRefs[0].Name) != "model-endpoint" {
		t.Fatalf("rules = %#v", got.Spec.Rules)
	}
	endpoint, err := p.Endpoint(ctx, pub)
	if err != nil || endpoint != "http://10.10.1.67:30090/v1" {
		t.Fatalf("Endpoint() = %q, %v", endpoint, err)
	}
}

func TestHTTPRoutePublisherConfirmPublishedRequiresBothConditions(t *testing.T) {
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "model-endpoint", 80)
	route.Status.Parents = []gatewayv1.RouteParentStatus{{
		ParentRef:  gatewayv1.ParentReference{Name: gatewayv1.ObjectName("ani-apisix"), Namespace: ptrNamespace("ingress-apisix")},
		Conditions: []metav1.Condition{{Type: string(gatewayv1.RouteConditionAccepted), Status: metav1.ConditionTrue}},
	}}
	if err := p.Client.Create(context.Background(), route); err != nil {
		t.Fatalf("create route: %v", err)
	}
	confirmed, err := p.ConfirmPublished(context.Background(), pub)
	if err != nil || confirmed {
		t.Fatalf("ConfirmPublished() = %v, %v; want false without ResolvedRefs", confirmed, err)
	}
	route.Status.Parents[0].Conditions = append(route.Status.Parents[0].Conditions, metav1.Condition{Type: string(gatewayv1.RouteConditionResolvedRefs), Status: metav1.ConditionTrue})
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

func ptrNamespace(value string) *gatewayv1.Namespace {
	namespace := gatewayv1.Namespace(value)
	return &namespace
}
