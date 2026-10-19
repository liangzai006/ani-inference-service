package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var higressWasmPluginGVK = schema.GroupVersionKind{
	Group: "extensions.higress.io", Version: "v1alpha1", Kind: "WasmPlugin",
}

const (
	higressRateLimitQPS              int64 = 10
	higressRateLimitOwnersAnnotation       = "ani.liangzai006.io/rate-limit-owners"
	higressRateLimitMaxUpdateRetries       = 5
)

func routeOwnerID(namespace, name string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(name)
}

// ensureRateLimit records the publication before its Higress route is exposed.
// The WasmPlugin is updated with resource-version retries so simultaneous model
// publications do not overwrite one another.
func (p *HTTPRoutePublisher) ensureRateLimit(ctx context.Context, model, owner string) error {
	model, owner = strings.TrimSpace(model), strings.TrimSpace(owner)
	if model == "" || owner == "" {
		return errors.New("Higress rate-limit model and owner are required")
	}
	for attempt := 0; attempt < higressRateLimitMaxUpdateRetries; attempt++ {
		plugin, err := p.getRateLimitPlugin(ctx)
		if err != nil {
			return err
		}
		base := plugin.DeepCopy()
		entries, keys, err := rateLimitEntries(plugin)
		if err != nil {
			return err
		}
		owners, err := rateLimitOwners(plugin)
		if err != nil {
			return err
		}
		changed := false
		_, exists := keys[model]
		if !exists {
			entries = append(entries, map[string]interface{}{"key": model, "query_per_second": higressRateLimitQPS})
			owners[model] = append(owners[model], owner)
			changed = true
		} else if _, managed := owners[model]; managed && !containsString(owners[model], owner) {
			owners[model] = append(owners[model], owner)
			changed = true
		}
		if !changed {
			return nil
		}
		if err := setRateLimitEntries(plugin, entries); err != nil {
			return err
		}
		if err := setRateLimitOwners(plugin, owners); err != nil {
			return err
		}
		if err := p.Client.Patch(ctx, plugin, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return fmt.Errorf("update Higress rate-limit plugin: %w", err)
		}
		return nil
	}
	return fmt.Errorf("update Higress rate-limit plugin conflicted after %d retries", higressRateLimitMaxUpdateRetries)
}

// cleanupRateLimit removes this route's ownership after the route is absent.
// A model key is removed only when no route still
// selects it. If it is the final key, it is retained because Higress rejects an
// empty limit_keys configuration; it then has no owner and is harmless until a
// platform operator replaces the bootstrap configuration.
func (p *HTTPRoutePublisher) cleanupRateLimit(ctx context.Context, owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return errors.New("Higress rate-limit owner is required")
	}
	for attempt := 0; attempt < higressRateLimitMaxUpdateRetries; attempt++ {
		plugin, err := p.getRateLimitPlugin(ctx)
		if err != nil {
			return err
		}
		base := plugin.DeepCopy()
		owners, err := rateLimitOwners(plugin)
		if err != nil {
			return err
		}
		model := ""
		for candidate, refs := range owners {
			if containsString(refs, owner) {
				model = candidate
				break
			}
		}
		if model == "" {
			return nil
		}

		refs := removeString(owners[model], owner)
		if len(refs) > 0 {
			owners[model] = refs
		} else {
			delete(owners, model)
			keep, err := p.hasModelRoute(ctx, model, owner)
			if err != nil {
				return err
			}
			if !keep {
				entries, keys, err := rateLimitEntries(plugin)
				if err != nil {
					return err
				}
				if len(keys) > 1 {
					entries = removeRateLimitEntry(entries, model)
					if err := setRateLimitEntries(plugin, entries); err != nil {
						return err
					}
				}
			}
		}
		if err := setRateLimitOwners(plugin, owners); err != nil {
			return err
		}
		if err := p.Client.Patch(ctx, plugin, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return fmt.Errorf("update Higress rate-limit plugin: %w", err)
		}
		return nil
	}
	return fmt.Errorf("cleanup Higress rate-limit plugin conflicted after %d retries", higressRateLimitMaxUpdateRetries)
}

func (p *HTTPRoutePublisher) getRateLimitPlugin(ctx context.Context) (*unstructured.Unstructured, error) {
	plugin := &unstructured.Unstructured{}
	plugin.SetGroupVersionKind(higressWasmPluginGVK)
	key := client.ObjectKey{Namespace: p.rateLimitNamespace(), Name: p.rateLimitPluginName()}
	if err := p.reader().Get(ctx, key, plugin); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Higress rate-limit WasmPlugin %s/%s not found: %w", key.Namespace, key.Name, err)
		}
		return nil, fmt.Errorf("read Higress rate-limit WasmPlugin %s/%s: %w", key.Namespace, key.Name, err)
	}
	return plugin, nil
}

func (p *HTTPRoutePublisher) rateLimitNamespace() string {
	if namespace := strings.TrimSpace(p.RateLimitNamespace); namespace != "" {
		return namespace
	}
	if namespace := strings.TrimSpace(p.GatewayNamespace); namespace != "" {
		return namespace
	}
	return "higress-system"
}

func (p *HTTPRoutePublisher) rateLimitPluginName() string {
	if name := strings.TrimSpace(p.RateLimitPlugin); name != "" {
		return name
	}
	return "ani-request-rate-limit"
}

func (p *HTTPRoutePublisher) hasModelRoute(ctx context.Context, model, withdrawnOwner string) (bool, error) {
	if p.Console != nil {
		routes, err := p.Console.ListRoutes(ctx)
		if err != nil {
			return false, fmt.Errorf("list Higress routes while cleaning model %q: %w", model, err)
		}
		for _, route := range routes {
			if strings.TrimSpace(fmt.Sprint(route["name"])) == strings.TrimSpace(withdrawnOwner) {
				continue
			}
			if nativeRouteSelectsModel(route, model) {
				return true, nil
			}
		}
		return false, nil
	}
	namespace, routeName, found := strings.Cut(withdrawnOwner, "/")
	if !found || namespace == "" || routeName == "" {
		return false, fmt.Errorf("invalid rate-limit owner %q", withdrawnOwner)
	}
	routes := &gatewayv1.HTTPRouteList{}
	if err := p.reader().List(ctx, routes, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("list HTTPRoutes while cleaning model %q: %w", model, err)
	}
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Name == routeName || route.GetDeletionTimestamp() != nil {
			continue
		}
		if routeSelectsModel(route, model) {
			return true, nil
		}
	}
	return false, nil
}

func nativeRouteSelectsModel(route map[string]any, model string) bool {
	headers, ok := route["headers"].([]any)
	if !ok {
		if typed, ok := route["headers"].([]map[string]any); ok {
			for _, header := range typed {
				if header["key"] == "x-higress-llm-model" && header["matchValue"] == model {
					return true
				}
			}
		}
		return false
	}
	for _, raw := range headers {
		header, ok := raw.(map[string]any)
		if ok && header["key"] == "x-higress-llm-model" && header["matchValue"] == model {
			return true
		}
	}
	return false
}

func routeSelectsModel(route *gatewayv1.HTTPRoute, model string) bool {
	for _, rule := range route.Spec.Rules {
		for _, match := range rule.Matches {
			for _, header := range match.Headers {
				if string(header.Name) == "x-higress-llm-model" && header.Value == model {
					return true
				}
			}
		}
	}
	return false
}

func rateLimitEntries(plugin *unstructured.Unstructured) ([]interface{}, map[string]int64, error) {
	entries, found, err := unstructured.NestedSlice(plugin.Object, "spec", "defaultConfig", "limit_keys")
	if err != nil {
		return nil, nil, fmt.Errorf("read Higress rate-limit keys: %w", err)
	}
	if !found {
		return []interface{}{}, map[string]int64{}, nil
	}
	keys := make(map[string]int64, len(entries))
	for i, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("Higress rate-limit key %d is not an object", i)
		}
		key, ok := entry["key"].(string)
		if !ok || strings.TrimSpace(key) == "" {
			return nil, nil, fmt.Errorf("Higress rate-limit key %d has no key", i)
		}
		qps, ok := int64Value(entry["query_per_second"])
		if !ok || qps < 1 {
			return nil, nil, fmt.Errorf("Higress rate-limit key %q has invalid query_per_second", key)
		}
		keys[key] = qps
	}
	return entries, keys, nil
}

func rateLimitKeys(plugin *unstructured.Unstructured) (map[string]int64, error) {
	_, keys, err := rateLimitEntries(plugin)
	return keys, err
}

func setRateLimitEntries(plugin *unstructured.Unstructured, entries []interface{}) error {
	return unstructured.SetNestedSlice(plugin.Object, entries, "spec", "defaultConfig", "limit_keys")
}

func rateLimitOwners(plugin *unstructured.Unstructured) (map[string][]string, error) {
	owners := map[string][]string{}
	value := plugin.GetAnnotations()[higressRateLimitOwnersAnnotation]
	if strings.TrimSpace(value) == "" {
		return owners, nil
	}
	if err := json.Unmarshal([]byte(value), &owners); err != nil {
		return nil, fmt.Errorf("decode Higress rate-limit ownership annotation: %w", err)
	}
	for model, refs := range owners {
		if strings.TrimSpace(model) == "" {
			return nil, errors.New("Higress rate-limit ownership contains an empty model")
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref) == "" {
				return nil, fmt.Errorf("Higress rate-limit ownership for %q contains an empty owner", model)
			}
		}
		sort.Strings(refs)
	}
	return owners, nil
}

func setRateLimitOwners(plugin *unstructured.Unstructured, owners map[string][]string) error {
	annotations := plugin.GetAnnotations()
	if len(owners) == 0 {
		delete(annotations, higressRateLimitOwnersAnnotation)
	} else {
		for model, refs := range owners {
			sort.Strings(refs)
			owners[model] = uniqueStrings(refs)
		}
		encoded, err := json.Marshal(owners)
		if err != nil {
			return fmt.Errorf("encode Higress rate-limit ownership annotation: %w", err)
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[higressRateLimitOwnersAnnotation] = string(encoded)
	}
	plugin.SetAnnotations(annotations)
	return nil
}

func removeRateLimitEntry(entries []interface{}, model string) []interface{} {
	kept := make([]interface{}, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok || entry["key"] != model {
			kept = append(kept, raw)
		}
	}
	return kept
}

func int64Value(value interface{}) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), value == float64(int64(value))
	case json.Number:
		n, err := value.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func removeString(values []string, unwanted string) []string {
	kept := values[:0]
	for _, value := range values {
		if value != unwanted {
			kept = append(kept, value)
		}
	}
	return uniqueStrings(kept)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
