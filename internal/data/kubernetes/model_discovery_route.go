package kubernetes

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// EnsureModelDiscoveryRoute sends only GET /v1/models to the AK-aware gateway
// adapter. Credentials reach that adapter, never an inference model container.
// Keep it in the adapter's namespace so no cross-namespace grant is needed.
func (p *HTTPRoutePublisher) EnsureModelDiscoveryRoute(ctx context.Context, namespace, serviceName string, servicePort int) error {
	route := &gatewayv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{APIVersion: gatewayv1.GroupVersion.String(), Kind: "HTTPRoute"},
		ObjectMeta: metav1.ObjectMeta{Name: "ani-inference-models", Namespace: namespace, Labels: map[string]string{
			managedByLabel: managedByInference, routeRoleLabel: "model-discovery",
		}},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
				Name: gatewayv1.ObjectName(p.GatewayName), Namespace: ptr.To(gatewayv1.Namespace(p.GatewayNamespace)), SectionName: ptr.To(gatewayv1.SectionName("http")),
			}}},
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{{
					Path:   &gatewayv1.HTTPPathMatch{Type: ptr.To(gatewayv1.PathMatchExact), Value: ptr.To("/v1/models")},
					Method: ptr.To(gatewayv1.HTTPMethod("GET")),
				}},
				BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{Name: gatewayv1.ObjectName(serviceName), Port: ptr.To(gatewayv1.PortNumber(servicePort))},
				}}},
			}},
		},
	}
	current := &gatewayv1.HTTPRoute{}
	key := client.ObjectKeyFromObject(route)
	if err := p.reader().Get(ctx, key, current); err == nil {
		if current.Labels[managedByLabel] != managedByInference || current.Labels[routeRoleLabel] != "model-discovery" {
			return errors.New("model discovery route is owned by a different controller")
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return p.Client.Patch(ctx, route, client.Apply, client.FieldOwner("ani-inference-model-discovery"))
}
