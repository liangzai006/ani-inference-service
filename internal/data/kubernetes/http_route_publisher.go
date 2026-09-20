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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
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
	PublicBaseURL    string
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
	return p.Client.Patch(ctx, route, client.Apply, client.FieldOwner(owner))
}

func (p *HTTPRoutePublisher) Withdraw(ctx context.Context, pub publication.Publication) error {
	if err := p.validate(pub); err != nil {
		return err
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
	route := p.emptyRoute(pub, p.routeNamespace(p.RouteNamespace))
	current := &gatewayv1.HTTPRoute{}
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
	if p.GatewayName == "" || p.GatewayNamespace == "" {
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

func (p *HTTPRoutePublisher) route(pub publication.Publication, namespace, backendName string, backendPort int32) *gatewayv1.HTTPRoute {
	route := p.emptyRoute(pub, namespace)
	route.SetLabels(map[string]string{
		tenantIDLabel:                   pub.TenantID,
		serviceIDLabel:                  pub.ServiceID,
		generationLabel:                 strconv.FormatInt(pub.Generation, 10),
		"ani.kubercloud.com/managed-by": "ani-inference",
	})
	pathType := gatewayv1.PathMatchPathPrefix
	path := p.pathPrefix()
	weight := int32(1)
	port := gatewayv1.PortNumber(backendPort)
	route.Spec = gatewayv1.HTTPRouteSpec{
		CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
			Name:        gatewayv1.ObjectName(p.GatewayName),
			Namespace:   ptr.To(gatewayv1.Namespace(p.GatewayNamespace)),
			SectionName: ptr.To(gatewayv1.SectionName("http")),
		}}},
		Rules: []gatewayv1.HTTPRouteRule{{
			Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &path}}},
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
