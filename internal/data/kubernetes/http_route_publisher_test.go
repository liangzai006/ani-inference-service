package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/publication"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestNativeRoutePayloadUsesModelHeaderAndServiceSource(t *testing.T) {
	route := nativeRoutePayload("route-1", "/v1/completions", "qwen", "source-1", 100)
	if route["name"] != "route-1" {
		t.Fatal("missing route name")
	}
	path := route["path"].(map[string]any)
	if path["matchType"] != "PRE" || path["matchValue"] != "/v1/completions" {
		t.Fatalf("path = %#v", path)
	}
	headers := route["headers"].([]map[string]any)
	if headers[0]["key"] != "x-higress-llm-model" || headers[0]["matchType"] != "EQUAL" || headers[0]["matchValue"] != "qwen" {
		t.Fatalf("headers = %#v", headers)
	}
	services := route["services"].([]map[string]any)
	if services[0]["name"] != "source-1" || services[0]["port"] != 100 || services[0]["weight"] != 100 {
		t.Fatalf("services = %#v", services)
	}
	source := nativeServiceSourcePayload("source-1", "svc.models.svc.cluster.local", 100)
	if source["type"] != "dns" || source["domain"] != "svc.models.svc.cluster.local" || source["port"] != 100 || source["protocol"] != "http" {
		t.Fatalf("source = %#v", source)
	}
	if _, found := source["address"]; found {
		t.Fatalf("source uses unsupported address field: %#v", source)
	}
}

func TestHTTPRoutePublisherNativePublishLifecycle(t *testing.T) {
	var requests []string
	var p *HTTPRoutePublisher
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
		switch {
		case r.Method == http.MethodPut && len(requests) == 1:
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && len(requests) == 2:
			assertRateLimitOwner(t, p, "served-model", nativeRouteName(testPublication()))
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"name": nativeRouteName(testPublication())}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p = testPublisher()
	p.Console = newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	pub := testPublication()
	if err := p.Publish(context.Background(), pub); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if len(requests) < 2 || requests[0] == requests[1] {
		t.Fatalf("native publish requests = %#v", requests)
	}
	if requests[0] != "PUT /v1/service-sources/"+nativeServiceSourceName(pub) || requests[1] != "PUT /v1/routes/"+nativeRouteName(pub) {
		t.Fatalf("native publish requests = %#v", requests)
	}
	assertRateLimitOwner(t, p, "served-model", nativeRouteName(pub))
	confirmed, err := p.ConfirmPublished(context.Background(), pub)
	if err != nil || !confirmed {
		t.Fatalf("ConfirmPublished() = %v, %v", confirmed, err)
	}
}

func TestHTTPRoutePublisherNativeRouteFailureRollsBack(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, fmt.Sprintf("%s %s", r.Method, r.URL.Path))
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/service-sources/"+nativeServiceSourceName(testPublication()):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/routes/"+nativeRouteName(testPublication()):
			w.WriteHeader(http.StatusBadGateway)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/routes":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []map[string]any{}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/service-sources/"+nativeServiceSourceName(testPublication()):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p := testPublisher()
	p.Console = newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	err := p.Publish(context.Background(), testPublication())
	if err == nil {
		t.Fatal("Publish() error = nil, want route failure")
	}
	if len(requests) != 4 || requests[0] != "PUT /v1/service-sources/"+nativeServiceSourceName(testPublication()) || requests[1] != "PUT /v1/routes/"+nativeRouteName(testPublication()) || requests[2] != "GET /v1/routes" || requests[3] != "DELETE /v1/service-sources/"+nativeServiceSourceName(testPublication()) {
		t.Fatalf("rollback requests = %#v", requests)
	}
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20})
	assertRateLimitOwner(t, p, "served-model")
}

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
	scheme.AddKnownTypeWithName(higressWasmPluginGVK, &unstructured.Unstructured{})
	return &HTTPRoutePublisher{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{testRateLimitPlugin()}, objects...)...).Build(),
		Source: publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
			TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models",
			ServedModelName: "served-model",
			Endpoint:        &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
		}}},
		GatewayNamespace: "ingress-higress", GatewayName: "ani-higress",
		RouteNamespace: "models", PublicBaseURL: "http://10.10.1.67:30090", PathPrefix: "/v1/completions",
		RateLimitNamespace: "higress-system", RateLimitPlugin: "ani-request-rate-limit",
	}
}

func testRateLimitPlugin() *unstructured.Unstructured {
	plugin := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": higressWasmPluginGVK.GroupVersion().String(),
		"kind":       higressWasmPluginGVK.Kind,
		"metadata": map[string]interface{}{
			"name":      "ani-request-rate-limit",
			"namespace": "higress-system",
		},
		"spec": map[string]interface{}{
			"defaultConfig": map[string]interface{}{
				"limit_by_header": "x-higress-llm-model",
				"limit_keys": []interface{}{
					map[string]interface{}{"key": "existing-model", "query_per_second": int64(20)},
				},
			},
		},
	}}
	plugin.SetGroupVersionKind(higressWasmPluginGVK)
	return plugin
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
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20, "served-model": higressRateLimitQPS})
	assertRateLimitOwner(t, p, "served-model", routeOwnerID(p.routeNamespace("models"), routeName(pub)))
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

func TestHTTPRoutePublisherPublishRateLimitIsIdempotent(t *testing.T) {
	ctx := context.Background()
	p := testPublisher()
	pub := testPublication()
	if err := p.Publish(ctx, pub); err != nil {
		t.Fatalf("first Publish() error = %v", err)
	}
	if err := p.Publish(ctx, pub); err != nil {
		t.Fatalf("second Publish() error = %v", err)
	}
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20, "served-model": higressRateLimitQPS})
	assertRateLimitOwner(t, p, "served-model", routeOwnerID("models", routeName(pub)))
}

func TestHTTPRoutePublisherPreservesPreconfiguredModelLimit(t *testing.T) {
	ctx := context.Background()
	pub := testPublication()
	p := testPublisher()
	p.Source = publicationRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models",
		ServedModelName: "existing-model",
		Endpoint:        &EndpointSpec{ContainerPort: 8080, ServicePort: 80, TargetPort: intstr.FromInt(8080)},
	}}}
	if err := p.Publish(ctx, pub); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	assertRateLimitOwner(t, p, "existing-model")
	route := &gatewayv1.HTTPRoute{}
	if err := p.Client.Get(ctx, client.ObjectKey{Namespace: "models", Name: routeName(pub)}, route); err != nil {
		t.Fatalf("get route: %v", err)
	}
	route.SetUID("preconfigured-route-uid")
	if err := p.Client.Update(ctx, route); err != nil {
		t.Fatalf("update route UID: %v", err)
	}
	if err := p.Withdraw(ctx, pub); err != nil {
		t.Fatalf("Withdraw() error = %v", err)
	}
	confirmed, err := p.ConfirmWithdrawn(ctx, pub)
	if err != nil || !confirmed {
		t.Fatalf("ConfirmWithdrawn() = %v, %v; want true", confirmed, err)
	}
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20})
	assertRateLimitOwner(t, p, "existing-model")
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
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20})
	assertRateLimitOwner(t, p, "served-model")
	if err := p.Withdraw(ctx, pub); err != nil {
		t.Fatalf("second Withdraw() error = %v", err)
	}
}

func TestHTTPRoutePublisherWithdrawKeepsSharedModelLimit(t *testing.T) {
	ctx := context.Background()
	pub := testPublication()
	p := testPublisher()
	route := p.route(pub, "models", "service-endpoint", 80, "served-model")
	route.SetUID("route-uid")
	if err := p.Client.Create(ctx, route); err != nil {
		t.Fatalf("create route: %v", err)
	}
	shared := p.route(publication.Publication{TenantID: "other-tenant", ServiceID: "other-service", Generation: 1}, "models", "other-endpoint", 80, "served-model")
	shared.SetUID("shared-route-uid")
	if err := p.Client.Create(ctx, shared); err != nil {
		t.Fatalf("create shared route: %v", err)
	}
	if err := p.ensureRateLimit(ctx, "served-model", routeOwnerID("models", routeName(pub))); err != nil {
		t.Fatalf("ensureRateLimit() error = %v", err)
	}
	if err := p.Withdraw(ctx, pub); err != nil {
		t.Fatalf("Withdraw() error = %v", err)
	}
	confirmed, err := p.ConfirmWithdrawn(ctx, pub)
	if err != nil || !confirmed {
		t.Fatalf("ConfirmWithdrawn() = %v, %v; want true", confirmed, err)
	}
	assertRateLimitKeys(t, p, map[string]int64{"existing-model": 20, "served-model": higressRateLimitQPS})
	assertRateLimitOwner(t, p, "served-model")
}

func assertRateLimitKeys(t *testing.T, p *HTTPRoutePublisher, want map[string]int64) {
	t.Helper()
	plugin := &unstructured.Unstructured{}
	plugin.SetGroupVersionKind(higressWasmPluginGVK)
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: p.rateLimitNamespace(), Name: p.rateLimitPluginName()}, plugin); err != nil {
		t.Fatalf("get rate-limit plugin: %v", err)
	}
	keys, err := rateLimitKeys(plugin)
	if err != nil {
		t.Fatalf("rateLimitKeys() error = %v", err)
	}
	if len(keys) != len(want) {
		t.Fatalf("rate limit keys = %#v, want %#v", keys, want)
	}
	for key, qps := range want {
		if keys[key] != qps {
			t.Fatalf("rate limit key %q = %d, want %d (all=%#v)", key, keys[key], qps, keys)
		}
	}
}

func assertRateLimitOwner(t *testing.T, p *HTTPRoutePublisher, model string, want ...string) {
	t.Helper()
	plugin := &unstructured.Unstructured{}
	plugin.SetGroupVersionKind(higressWasmPluginGVK)
	if err := p.Client.Get(context.Background(), client.ObjectKey{Namespace: p.rateLimitNamespace(), Name: p.rateLimitPluginName()}, plugin); err != nil {
		t.Fatalf("get rate-limit plugin: %v", err)
	}
	owners, err := rateLimitOwners(plugin)
	if err != nil {
		t.Fatalf("rateLimitOwners() error = %v", err)
	}
	if len(owners[model]) != len(want) {
		t.Fatalf("rate limit owners[%q] = %#v, want %#v", model, owners[model], want)
	}
	for i := range want {
		if owners[model][i] != want[i] {
			t.Fatalf("rate limit owners[%q] = %#v, want %#v", model, owners[model], want)
		}
	}
}

func ptrNamespace(value string) *gatewayv1.Namespace {
	namespace := gatewayv1.Namespace(value)
	return &namespace
}
