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
			ServedModelName: "served-model",
			Endpoint:        &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
		}}},
		GatewayNamespace: "ingress-higress", GatewayName: "ani-higress",
		RouteNamespace: "models", PublicBaseURL: "http://10.10.1.67:30090", PathPrefix: "/v1/completions",
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
	if len(got.Spec.ParentRefs) != 1 || string(got.Spec.ParentRefs[0].Name) != "ani-higress" {
		t.Fatalf("parentRefs = %#v", got.Spec.ParentRefs)
	}
	if len(got.Spec.Hostnames) != 0 {
		t.Fatalf("hostnames = %#v; want catch-all route", got.Spec.Hostnames)
	}
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].BackendRefs) != 1 || string(got.Spec.Rules[0].BackendRefs[0].Name) != "service-endpoint" {
		t.Fatalf("rules = %#v", got.Spec.Rules)
	}
	if len(got.Spec.Rules[0].Matches) != 1 {
		t.Fatalf("matches = %#v", got.Spec.Rules[0].Matches)
	}
	match := got.Spec.Rules[0].Matches[0]
	if match.Path == nil || match.Path.Type == nil || *match.Path.Type != gatewayv1.PathMatchPathPrefix || match.Path.Value == nil || *match.Path.Value != "/v1/completions" {
		t.Fatalf("path match = %#v", match.Path)
	}
	if len(match.Headers) != 1 || match.Headers[0].Name != "x-higress-llm-model" || match.Headers[0].Value != "served-model" || match.Headers[0].Type == nil || *match.Headers[0].Type != gatewayv1.HeaderMatchExact {
		t.Fatalf("header match = %#v", match.Headers)
	}
	endpoint, err := p.Endpoint(ctx, pub)
	if err != nil || endpoint != "http://10.10.1.67:30090/v1/completions" {
		t.Fatalf("Endpoint() = %q, %v", endpoint, err)
	}
}

func TestHTTPRoutePublisherUsesKServePredictorService(t *testing.T) {
	p := testPublisher()
	p.RuntimeProvider = "kserve"
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", ServedModelName: "served-model",
		Endpoint: &EndpointSpec{ContainerPort: 9000, ServicePort: 1234, TargetPort: intstr.FromInt(9000)},
	}}}
	if err := p.Publish(context.Background(), testPublication()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	got := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "models", Name: routeName(testPublication())}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].BackendRefs) != 1 {
		t.Fatalf("rules = %#v", got.Spec.Rules)
	}
	if name := string(got.Spec.Rules[0].BackendRefs[0].Name); name != "model-predictor" {
		t.Fatalf("backend name = %q, want model-predictor", name)
	}
	if port := got.Spec.Rules[0].BackendRefs[0].Port; port == nil || int32(*port) != KServePredictorServicePort {
		t.Fatalf("backend port = %v, want %d", port, KServePredictorServicePort)
	}
}

func TestHTTPRoutePublisherUsesKServeLLMWorkloadService(t *testing.T) {
	p := testPublisher()
	p.RuntimeProvider = "kserve"
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "distributed", Namespace: "models", ServedModelName: "served-model",
		RuntimeMode: "leader_worker_set",
		Endpoint:    &EndpointSpec{ContainerPort: 9000, ServicePort: 1234, TargetPort: intstr.FromInt(9000)},
	}}}
	if err := p.Publish(context.Background(), testPublication()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	got := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "models", Name: routeName(testPublication())}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	if len(got.Spec.Rules) != 1 || len(got.Spec.Rules[0].BackendRefs) != 1 {
		t.Fatalf("rules = %#v", got.Spec.Rules)
	}
	ref := got.Spec.Rules[0].BackendRefs[0]
	if name := string(ref.Name); name != KServeLLMWorkloadServiceName("distributed") {
		t.Fatalf("backend name = %q, want %q", name, KServeLLMWorkloadServiceName("distributed"))
	}
	if ref.Port == nil || int32(*ref.Port) != KServeLLMWorkloadServicePort {
		t.Fatalf("backend port = %v, want %d", ref.Port, KServeLLMWorkloadServicePort)
	}
}

func TestHTTPRoutePublisherKeepsOldKServePredictorDuringTopologyReplacement(t *testing.T) {
	p := testPublisher()
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "distributed", Namespace: "models", ServedModelName: "served-model",
		RuntimeProvider: "kserve", RuntimeMode: "leader_worker_set",
	}, Bindings: []RuntimeBinding{{Kind: KServeInferenceServiceKind, Role: "runtime"}}}}
	if err := p.Publish(context.Background(), testPublication()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	got := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "models", Name: routeName(testPublication())}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	ref := got.Spec.Rules[0].BackendRefs[0]
	if string(ref.Name) != KServePredictorServiceName("distributed") || ref.Port == nil || int32(*ref.Port) != KServePredictorServicePort {
		t.Fatalf("backend = %s:%v; want old predictor:%d", ref.Name, ref.Port, KServePredictorServicePort)
	}
}

func TestHTTPRoutePublisherUsesKServeBindingWhenProviderOmitted(t *testing.T) {
	p := testPublisher()
	p.RuntimeProvider = ""
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "bound", Namespace: "models", ServedModelName: "served-model",
		RuntimeMode: "leader_worker_set",
	}, Bindings: []RuntimeBinding{{Kind: KServeLLMInferenceServiceKind, Role: "runtime"}}}}
	if err := p.Publish(context.Background(), testPublication()); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	got := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: "models", Name: routeName(testPublication())}, got); err != nil {
		t.Fatalf("get route: %v", err)
	}
	ref := got.Spec.Rules[0].BackendRefs[0]
	if string(ref.Name) != KServeLLMWorkloadServiceName("bound") || ref.Port == nil || int32(*ref.Port) != KServeLLMWorkloadServicePort {
		t.Fatalf("backend = %s:%v, want %s:%d", ref.Name, ref.Port, KServeLLMWorkloadServiceName("bound"), KServeLLMWorkloadServicePort)
	}
}

func TestHTTPRoutePublisherConfirmPublishedRequiresBothConditions(t *testing.T) {
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "service-endpoint", 80, "served-model")
	route.Status.Parents = []gatewayv1.RouteParentStatus{{
		ParentRef:  gatewayv1.ParentReference{Name: gatewayv1.ObjectName("ani-higress"), Namespace: ptrNamespace("ingress-higress")},
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

func TestHTTPRoutePublisherRejectsEmptyServedModelName(t *testing.T) {
	p := testPublisher()
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models",
		Endpoint: &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
	}}}
	if err := p.Publish(context.Background(), testPublication()); err == nil {
		t.Fatal("Publish() error = nil, want empty served model rejection")
	}
}

func TestHTTPRoutePublisherWithdrawIsFencedAndConvergent(t *testing.T) {
	ctx := context.Background()
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "service-endpoint", 80, "served-model")
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
