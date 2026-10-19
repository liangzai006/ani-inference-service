package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/liangzai006/ani-inference-service/internal/biz/publication"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// HTTPRoutePublisher owns the Higress publication boundary for one Inference
// service. The type name remains for compatibility with the business port.
type HTTPRoutePublisher struct {
	Client    client.Client
	APIReader client.Reader
	Source    DesiredRuntimeSource
	// Console selects the native Higress route API. It remains optional while
	// process wiring migrates; an unset client uses the legacy Gateway API path.
	Console *HigressConsoleClient
	// RuntimeProvider selects the backend naming contract. The legacy
	// deployment provider owns <service-id>-endpoint; KServe owns its
	// predictor/workload Service.
	RuntimeProvider string

	GatewayNamespace   string
	GatewayName        string
	RouteNamespace     string
	PublicBaseURL      string
	PathPrefix         string
	FieldManager       string
	RateLimitNamespace string
	RateLimitPlugin    string
}

var _ publication.Port = (*HTTPRoutePublisher)(nil)
var _ publication.EndpointResolver = (*HTTPRoutePublisher)(nil)

func (p *HTTPRoutePublisher) Publish(ctx context.Context, pub publication.Publication) error {
	if err := p.validate(pub); err != nil {
		return err
	}
	if p.Source == nil {
		return errors.New("publication desired runtime source is nil")
	}
	desired, err := p.Source.CurrentRuntime(ctx, pub.TenantID, pub.ServiceID, pub.Generation)
	if err != nil {
		return err
	}
	if strings.TrimSpace(desired.ServedModelName) == "" {
		return errors.New("publication served model name is required")
	}
	if desired.Namespace == "" || desired.Name == "" {
		return errors.New("publication runtime namespace and name are required")
	}
	if p.RouteNamespace != "" && p.RouteNamespace != desired.Namespace {
		return fmt.Errorf("publication route namespace %q does not match runtime namespace %q", p.RouteNamespace, desired.Namespace)
	}
	runtimeProvider := strings.ToLower(strings.TrimSpace(desired.RuntimeProvider))
	if runtimeProvider == "" {
		// A persisted runtime binding is authoritative while a generation is
		// being migrated. This mirrors RuntimeRouter and keeps publication
		// pointed at the old KServe object during a provider transition.
		for _, binding := range desired.Bindings {
			if binding.Role != "" && binding.Role != "runtime" {
				continue
			}
			switch binding.Kind {
			case KServeInferenceServiceKind, KServeLLMInferenceServiceKind:
				runtimeProvider = "kserve"
			case "Deployment", "LeaderWorkerSet":
				runtimeProvider = "deployment"
			}
			if runtimeProvider != "" {
				break
			}
		}
	}
	if runtimeProvider == "" {
		// Keep the field for compatibility with older process configuration while
		// persisted generations are being migrated. Once a generation has a
		// provider, that value always wins.
		runtimeProvider = strings.ToLower(strings.TrimSpace(p.RuntimeProvider))
	}
	if runtimeProvider == "" {
		runtimeProvider = "deployment"
	}
	if runtimeProvider != "deployment" && runtimeProvider != "kserve" {
		return fmt.Errorf("unsupported publication runtime provider %q", runtimeProvider)
	}
	servicePort := desired.ServicePort
	backendName := pub.ServiceID + "-endpoint"
	if runtimeProvider == "kserve" {
		kserveKind := ""
		for _, binding := range desired.Bindings {
			if binding.Role == "" || binding.Role == "runtime" {
				if isKServeBindingKind(binding.Kind) {
					kserveKind = binding.Kind
					break
				}
			}
		}
		// During a provider/topology replacement the persisted binding is the
		// object that still exists. Keep the route on that object's Service until
		// the new KServe binding has been durably recorded.
		if kserveKind == KServeLLMInferenceServiceKind || (kserveKind == "" && desired.RuntimeMode == "leader_worker_set") {
			backendName = KServeLLMWorkloadServiceName(desired.Name)
			// LLMInferenceService exposes its generated workload Service on
			// the OpenAI-compatible HTTP port 8000.
			servicePort = KServeLLMWorkloadServicePort
		} else {
			backendName = KServePredictorServiceName(desired.Name)
			// KServe's predictor Service exposes the HTTP predictor on its
			// stable service port 80; the user endpoint port remains the
			// container port.
			servicePort = KServePredictorServicePort
		}
	}
	if desired.Endpoint != nil && runtimeProvider != "kserve" {
		servicePort = desired.Endpoint.ServicePort
	}
	if servicePort < 1 || servicePort > 65535 {
		return fmt.Errorf("publication backend service port %d is invalid", servicePort)
	}
	if p.Console != nil {
		routeName := nativeRouteName(pub)
		sourceName := nativeServiceSourceName(pub)
		address := fmt.Sprintf("%s.%s.svc.cluster.local", backendName, desired.Namespace)
		source := nativeServiceSourcePayload(sourceName, address, int(servicePort))
		if err := p.Console.PutServiceSource(ctx, sourceName, source); err != nil {
			return err
		}
		// Higress's native route API resolves services by their DNS name. The
		// Service Source is retained as the publication's managed endpoint
		// record, while the route points directly at the predictor Service.
		route := nativeRoutePayload(routeName, p.pathPrefix(), desired.ServedModelName, address, int(servicePort))
		route["customLabels"] = publicationLabels(pub)
		if err := p.ensureRateLimit(ctx, desired.ServedModelName, routeName); err != nil {
			_ = p.Console.DeleteServiceSource(ctx, sourceName)
			return err
		}
		if err := p.Console.PutRoute(ctx, routeName, route); err != nil {
			_ = p.cleanupRateLimit(ctx, routeName)
			_ = p.Console.DeleteServiceSource(ctx, sourceName)
			return err
		}
		return nil
	}
	routeNamespace := p.routeNamespace(desired.Namespace)
	route := p.route(pub, routeNamespace, backendName, servicePort, desired.ServedModelName)
	reader := p.reader()
	current := &gatewayv1.HTTPRoute{}
	key := client.ObjectKey{Namespace: routeNamespace, Name: route.GetName()}
	if err := reader.Get(ctx, key, current); err == nil {
		if !samePublication(current, pub) {
			return errors.New("HTTPRoute is owned by a different publication")
		}
		if current.GetDeletionTimestamp() != nil {
			return errors.New("HTTPRoute deletion is still in progress")
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	owner := p.FieldManager
	if owner == "" {
		owner = "ani-inference-publication"
	}
	if err := p.ensureRateLimit(ctx, desired.ServedModelName, routeOwnerID(routeNamespace, route.GetName())); err != nil {
		return err
	}
	return p.Client.Patch(ctx, route, client.Apply, client.FieldOwner(owner))
}

func (p *HTTPRoutePublisher) Withdraw(ctx context.Context, pub publication.Publication) error {
	if err := p.validate(pub); err != nil {
		return err
	}
	if p.Console != nil {
		name := nativeRouteName(pub)
		if err := p.Console.DeleteRoute(ctx, name); err != nil {
			return err
		}
		return p.Console.DeleteServiceSource(ctx, nativeServiceSourceName(pub))
	}
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	reader := p.reader()
	current := &gatewayv1.HTTPRoute{}
	key := client.ObjectKey{Namespace: route.GetNamespace(), Name: route.GetName()}
	if err := reader.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !samePublication(current, pub) {
		return errors.New("HTTPRoute is owned by a different publication")
	}
	uid, resourceVersion := current.GetUID(), current.GetResourceVersion()
	if uid == "" || resourceVersion == "" {
		return errors.New("HTTPRoute is missing UID or resourceVersion")
	}
	return p.Client.Delete(ctx, current, client.Preconditions{UID: &uid, ResourceVersion: &resourceVersion})
}

func (p *HTTPRoutePublisher) ConfirmWithdrawn(ctx context.Context, pub publication.Publication) (bool, error) {
	if err := p.validate(pub); err != nil {
		return false, err
	}
	if p.Console != nil {
		_, err := p.Console.GetRoute(ctx, nativeRouteName(pub))
		if err != nil {
			var statusErr *higressConsoleStatusError
			if errors.As(err, &statusErr) && statusErr.Status == 404 {
				if err := p.cleanupRateLimit(ctx, nativeRouteName(pub)); err != nil {
					return false, err
				}
				return true, nil
			}
			return false, err
		}
		return false, nil
	}
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	current := &gatewayv1.HTTPRoute{}
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(route), current); err != nil {
		if apierrors.IsNotFound(err) {
			if err := p.cleanupRateLimit(ctx, routeOwnerID(route.GetNamespace(), route.GetName())); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (p *HTTPRoutePublisher) ConfirmPublished(ctx context.Context, pub publication.Publication) (bool, error) {
	if err := p.validate(pub); err != nil {
		return false, err
	}
	if p.Console != nil {
		route, err := p.Console.GetRoute(ctx, nativeRouteName(pub))
		if err != nil {
			var statusErr *higressConsoleStatusError
			if errors.As(err, &statusErr) && statusErr.Status == 404 {
				return false, nil
			}
			return false, err
		}
		return route != nil, nil
	}
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	current := &gatewayv1.HTTPRoute{}
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(route), current); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !samePublication(current, pub) {
		return false, errors.New("HTTPRoute is owned by a different publication")
	}
	accepted, resolved := false, false
	for _, parent := range current.Status.Parents {
		if !p.matchesGatewayParent(parent, current.GetNamespace()) {
			continue
		}
		for _, condition := range parent.Conditions {
			if condition.Status != metav1.ConditionTrue {
				continue
			}
			switch condition.Type {
			case string(gatewayv1.RouteConditionAccepted):
				accepted = true
			case string(gatewayv1.RouteConditionResolvedRefs):
				resolved = true
			}
		}
	}
	return accepted && resolved, nil
}

func (p *HTTPRoutePublisher) Endpoint(_ context.Context, pub publication.Publication) (string, error) {
	if err := p.validate(pub); err != nil {
		return "", err
	}
	base, err := url.Parse(strings.TrimSpace(p.PublicBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("publication public base URL must include scheme and host")
	}
	base.Path = strings.TrimRight(base.Path, "/") + p.pathPrefix()
	base.RawQuery, base.Fragment = "", ""
	return base.String(), nil
}

func (p *HTTPRoutePublisher) validate(pub publication.Publication) error {
	if p == nil || p.Client == nil {
		return errors.New("publication Kubernetes client is nil")
	}
	if pub.TenantID == "" || pub.ServiceID == "" || pub.Generation < 1 {
		return errors.New("publication tenant, service and positive generation are required")
	}
	if p.Console == nil && (p.GatewayName == "" || p.GatewayNamespace == "") {
		return errors.New("publication Gateway namespace and name are required")
	}
	if strings.TrimSpace(p.PublicBaseURL) == "" {
		return errors.New("publication public base URL is required")
	}
	return nil
}

func (p *HTTPRoutePublisher) reader() client.Reader {
	if p.APIReader != nil {
		return p.APIReader
	}
	return p.Client
}

func (p *HTTPRoutePublisher) routeNamespace(runtimeNamespace string) string {
	if p.RouteNamespace != "" {
		return p.RouteNamespace
	}
	return runtimeNamespace
}

func (p *HTTPRoutePublisher) emptyRoute(pub publication.Publication, namespace string) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{APIVersion: gatewayv1.SchemeGroupVersion.String(), Kind: "HTTPRoute"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      routeName(pub),
		},
	}
}

func (p *HTTPRoutePublisher) route(pub publication.Publication, namespace, backendName string, backendPort int32, servedModelName string) *gatewayv1.HTTPRoute {
	route := p.emptyRoute(pub, namespace)
	route.SetLabels(map[string]string{
		tenantIDLabel:                   pub.TenantID,
		serviceIDLabel:                  pub.ServiceID,
		generationLabel:                 strconv.FormatInt(pub.Generation, 10),
		"ani.kubercloud.com/managed-by": "ani-inference",
	})
	pathType := gatewayv1.PathMatchPathPrefix
	path := p.pathPrefix()
	headerType := gatewayv1.HeaderMatchExact
	weight := int32(1)
	port := gatewayv1.PortNumber(backendPort)
	route.Spec = gatewayv1.HTTPRouteSpec{
		CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
			Name:        gatewayv1.ObjectName(p.GatewayName),
			Namespace:   ptr.To(gatewayv1.Namespace(p.GatewayNamespace)),
			SectionName: ptr.To(gatewayv1.SectionName("http")),
		}}},
		Rules: []gatewayv1.HTTPRouteRule{{
			Matches: []gatewayv1.HTTPRouteMatch{{
				Path:    &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &path},
				Headers: []gatewayv1.HTTPHeaderMatch{{Type: &headerType, Name: "x-higress-llm-model", Value: servedModelName}},
			}},
			BackendRefs: []gatewayv1.HTTPBackendRef{{
				BackendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName(backendName),
						Port: &port,
					},
					Weight: &weight,
				},
			}},
		}},
	}
	return route
}

func samePublication(obj metav1.Object, pub publication.Publication) bool {
	labels := obj.GetLabels()
	return labels[tenantIDLabel] == pub.TenantID && labels[serviceIDLabel] == pub.ServiceID && labels[generationLabel] == strconv.FormatInt(pub.Generation, 10)
}

func (p *HTTPRoutePublisher) matchesGatewayParent(parent gatewayv1.RouteParentStatus, routeNamespace string) bool {
	if string(parent.ParentRef.Name) != p.GatewayName {
		return false
	}
	namespace := routeNamespace
	if parent.ParentRef.Namespace != nil && string(*parent.ParentRef.Namespace) != "" {
		namespace = string(*parent.ParentRef.Namespace)
	}
	return namespace == p.GatewayNamespace
}

func routeName(pub publication.Publication) string {
	sum := sha256.Sum256([]byte(pub.TenantID + "|" + pub.ServiceID + "|" + strconv.FormatInt(pub.Generation, 10)))
	return "ani-pub-" + hex.EncodeToString(sum[:])[:20]
}

func nativeRouteName(pub publication.Publication) string {
	return routeName(pub)
}

func nativeServiceSourceName(pub publication.Publication) string {
	sum := sha256.Sum256([]byte("service-source|" + pub.TenantID + "|" + pub.ServiceID + "|" + strconv.FormatInt(pub.Generation, 10)))
	return "ani-source-" + hex.EncodeToString(sum[:])[:20]
}

func nativeRoutePayload(name, path, model, source string, port int) map[string]any {
	return map[string]any{
		"name":     name,
		"path":     map[string]any{"matchType": "PRE", "matchValue": path},
		"headers":  []map[string]any{{"key": "x-higress-llm-model", "matchType": "EQUAL", "matchValue": model}},
		"services": []map[string]any{{"name": source, "port": port, "weight": 100}},
	}
}

// EnsureBootstrapRoute publishes the fixed path-only entry used by Higress's
// model-router plugin before it can select a model-specific route. The sink
// service has no endpoints, so an unknown model cannot fall through to a real
// inference service.
func (p *HTTPRoutePublisher) EnsureBootstrapRoute(ctx context.Context, routeName, sourceName, sinkServiceName string, servicePort int) error {
	if p.Console == nil {
		return errors.New("Higress Console client is required for bootstrap route")
	}
	if strings.TrimSpace(routeName) == "" || strings.TrimSpace(sourceName) == "" || strings.TrimSpace(sinkServiceName) == "" {
		return errors.New("Higress bootstrap route and service source names are required")
	}
	if servicePort < 1 || servicePort > 65535 {
		return fmt.Errorf("Higress bootstrap service port %d is invalid", servicePort)
	}
	namespace := strings.TrimSpace(p.RouteNamespace)
	if namespace == "" {
		return errors.New("Higress bootstrap route namespace is required")
	}
	address := fmt.Sprintf("%s.%s.svc.cluster.local", sinkServiceName, namespace)
	if err := p.Console.PutServiceSource(ctx, sourceName, nativeServiceSourcePayload(sourceName, address, servicePort)); err != nil {
		return err
	}
	route := map[string]any{
		"name":     routeName,
		"path":     map[string]any{"matchType": "PRE", "matchValue": p.pathPrefix()},
		"services": []map[string]any{{"name": sourceName, "port": servicePort, "weight": 100}},
	}
	if err := p.Console.PutRoute(ctx, routeName, route); err != nil {
		_ = p.Console.DeleteServiceSource(ctx, sourceName)
		return err
	}
	return nil
}

func nativeServiceSourcePayload(name, address string, port int) map[string]any {
	return map[string]any{
		"name":     name,
		"type":     "dns",
		"domain":   address,
		"port":     port,
		"protocol": "http",
	}
}

func publicationLabels(pub publication.Publication) map[string]string {
	return map[string]string{
		tenantIDLabel:                   pub.TenantID,
		serviceIDLabel:                  pub.ServiceID,
		generationLabel:                 strconv.FormatInt(pub.Generation, 10),
		"ani.kubercloud.com/managed-by": "ani-inference",
	}
}

func (p *HTTPRoutePublisher) pathPrefix() string {
	path := strings.TrimSpace(p.PathPrefix)
	if path == "" {
		path = "/v1/completions"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}
