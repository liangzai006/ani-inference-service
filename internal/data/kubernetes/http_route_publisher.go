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

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	httpRouteAPIVersion = "gateway.networking.k8s.io/v1"
	httpRouteKind       = "HTTPRoute"
)

// HTTPRoutePublisher owns the HTTPRoute boundary for one Inference service.
// APISIX is deliberately reached through the Gateway API; this adapter never
// calls the APISIX Admin API directly.
type HTTPRoutePublisher struct {
	Client    client.Client
	APIReader client.Reader
	Source    DesiredRuntimeSource

	GatewayNamespace string
	GatewayName      string
	RouteNamespace   string
	HostSuffix       string
	Scheme           string
	PathPrefix       string
	FieldManager     string
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
	if desired.Namespace == "" || desired.Name == "" {
		return errors.New("publication runtime namespace and name are required")
	}
	if p.RouteNamespace != "" && p.RouteNamespace != desired.Namespace {
		return fmt.Errorf("publication route namespace %q does not match runtime namespace %q", p.RouteNamespace, desired.Namespace)
	}
	servicePort := desired.ServicePort
	if desired.Endpoint != nil {
		servicePort = desired.Endpoint.ServicePort
	}
	if servicePort < 1 || servicePort > 65535 {
		return fmt.Errorf("publication backend service port %d is invalid", servicePort)
	}
	routeNamespace := p.routeNamespace(desired.Namespace)
	route := p.route(pub, routeNamespace, desired.Name+"-endpoint", servicePort)
	reader := p.reader()
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(route.GroupVersionKind())
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
	return p.Client.Patch(ctx, route, client.Apply, client.FieldOwner(owner))
}

func (p *HTTPRoutePublisher) Withdraw(ctx context.Context, pub publication.Publication) error {
	if err := p.validate(pub); err != nil {
		return err
	}
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	reader := p.reader()
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(route.GroupVersionKind())
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
	uid, resourceVersion := types.UID(current.GetUID()), current.GetResourceVersion()
	if uid == "" || resourceVersion == "" {
		return errors.New("HTTPRoute is missing UID or resourceVersion")
	}
	return p.Client.Delete(ctx, current, client.Preconditions{UID: &uid, ResourceVersion: &resourceVersion})
}

func (p *HTTPRoutePublisher) ConfirmWithdrawn(ctx context.Context, pub publication.Publication) (bool, error) {
	if err := p.validate(pub); err != nil {
		return false, err
	}
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(route.GroupVersionKind())
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(route), current); err != nil {
		if apierrors.IsNotFound(err) {
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
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	current := &unstructured.Unstructured{}
	current.SetGroupVersionKind(route.GroupVersionKind())
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(route), current); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !samePublication(current, pub) {
		return false, errors.New("HTTPRoute is owned by a different publication")
	}
	status, found, err := unstructured.NestedMap(current.Object, "status")
	if err != nil || !found {
		return false, err
	}
	parents, found, err := unstructured.NestedSlice(status, "parents")
	if err != nil || !found {
		return false, err
	}
	accepted, resolved := false, false
	for _, rawParent := range parents {
		parent, ok := rawParent.(map[string]interface{})
		if !ok || !p.matchesGatewayParent(parent, route.GetNamespace()) {
			continue
		}
		conditions, ok, err := unstructured.NestedSlice(parent, "conditions")
		if err != nil || !ok {
			continue
		}
		for _, rawCondition := range conditions {
			condition, ok := rawCondition.(map[string]interface{})
			if !ok || condition["status"] != "True" {
				continue
			}
			switch condition["type"] {
			case "Accepted":
				accepted = true
			case "ResolvedRefs":
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
	host := p.host(pub.ServiceID)
	path := p.pathPrefix()
	scheme := p.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: path}).String(), nil
}

func (p *HTTPRoutePublisher) validate(pub publication.Publication) error {
	if p == nil || p.Client == nil {
		return errors.New("publication Kubernetes client is nil")
	}
	if pub.TenantID == "" || pub.ServiceID == "" || pub.Generation < 1 {
		return errors.New("publication tenant, service and positive generation are required")
	}
	if p.GatewayName == "" || p.GatewayNamespace == "" {
		return errors.New("publication Gateway namespace and name are required")
	}
	if p.HostSuffix == "" {
		return errors.New("publication host suffix is required")
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

func (p *HTTPRoutePublisher) emptyRoute(pub publication.Publication, namespace string) *unstructured.Unstructured {
	route := &unstructured.Unstructured{}
	route.SetAPIVersion(httpRouteAPIVersion)
	route.SetKind(httpRouteKind)
	route.SetNamespace(namespace)
	route.SetName(routeName(pub))
	return route
}

func (p *HTTPRoutePublisher) route(pub publication.Publication, namespace, backendName string, backendPort int32) *unstructured.Unstructured {
	route := p.emptyRoute(pub, namespace)
	route.SetLabels(map[string]string{
		tenantIDLabel:                   pub.TenantID,
		serviceIDLabel:                  pub.ServiceID,
		generationLabel:                 strconv.FormatInt(pub.Generation, 10),
		"ani.kubercloud.com/managed-by": "ani-inference",
	})
	_ = unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
		"name":        p.GatewayName,
		"namespace":   p.GatewayNamespace,
		"sectionName": "http",
	}}, "spec", "parentRefs")
	_ = unstructured.SetNestedStringSlice(route.Object, []string{p.host(pub.ServiceID)}, "spec", "hostnames")
	_ = unstructured.SetNestedSlice(route.Object, []interface{}{map[string]interface{}{
		"matches": []interface{}{map[string]interface{}{"path": map[string]interface{}{"type": "PathPrefix", "value": p.pathPrefix()}}},
		"backendRefs": []interface{}{map[string]interface{}{
			"name":   backendName,
			"port":   int64(backendPort),
			"weight": int64(1),
		}},
	}}, "spec", "rules")
	return route
}

func (p *HTTPRoutePublisher) matchesGatewayParent(parent map[string]interface{}, routeNamespace string) bool {
	parentRef, ok := parent["parentRef"].(map[string]interface{})
	if !ok {
		return false
	}
	name, _ := parentRef["name"].(string)
	if name != p.GatewayName {
		return false
	}
	namespace, hasNamespace := parentRef["namespace"].(string)
	if !hasNamespace || namespace == "" {
		namespace = routeNamespace
	}
	return namespace == p.GatewayNamespace
}

func samePublication(obj *unstructured.Unstructured, pub publication.Publication) bool {
	labels := obj.GetLabels()
	return labels[tenantIDLabel] == pub.TenantID && labels[serviceIDLabel] == pub.ServiceID && labels[generationLabel] == strconv.FormatInt(pub.Generation, 10)
}

func routeName(pub publication.Publication) string {
	sum := sha256.Sum256([]byte(pub.TenantID + "|" + pub.ServiceID + "|" + strconv.FormatInt(pub.Generation, 10)))
	return "ani-pub-" + hex.EncodeToString(sum[:])[:20]
}

func (p *HTTPRoutePublisher) host(serviceID string) string {
	suffix := strings.Trim(strings.ToLower(p.HostSuffix), ".")
	return strings.ToLower(serviceID) + "." + suffix
}

func (p *HTTPRoutePublisher) pathPrefix() string {
	path := strings.TrimSpace(p.PathPrefix)
	if path == "" {
		path = "/v1"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}
