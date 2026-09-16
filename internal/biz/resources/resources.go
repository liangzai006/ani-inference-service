// Package resources validates the Kubernetes ResourceRequirements contract
// exposed by the Inference API.
package resources

import (
	"fmt"
	"sort"
	"strings"

	resource "k8s.io/apimachinery/pkg/api/resource"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

// Spec is the transport-neutral representation of Kubernetes resources.
type Spec struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

// Normalized contains canonical quantities and effective requests. Kubernetes
// treats an extended resource specified only in limits as having the same
// request, so Normalize makes that behavior explicit for quota accounting.
type Normalized struct {
	Requests map[string]string
	Limits   map[string]string
}

func Normalize(in Spec) (Normalized, error) {
	if len(in.Requests) == 0 && len(in.Limits) == 0 {
		return Normalized{Requests: map[string]string{}, Limits: map[string]string{}}, nil
	}
	keys := make(map[string]struct{}, len(in.Requests)+len(in.Limits))
	for k := range in.Requests {
		keys[k] = struct{}{}
	}
	for k := range in.Limits {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	requests := make(map[string]string, len(keys))
	limits := make(map[string]string, len(keys))
	for _, name := range ordered {
		if errs := utilvalidation.IsQualifiedName(name); len(errs) > 0 {
			return Normalized{}, fmt.Errorf("invalid resource name %q: %s", name, errs[0])
		}
		rawRequest, hasRequest := in.Requests[name]
		rawLimit, hasLimit := in.Limits[name]
		if !hasRequest && !hasLimit {
			continue
		}
		var rq, lq *resource.Quantity
		if hasRequest {
			q, err := parseQuantity(rawRequest)
			if err != nil {
				return Normalized{}, fmt.Errorf("invalid request %q for %s: %w", rawRequest, name, err)
			}
			rq = q
		}
		if hasLimit {
			q, err := parseQuantity(rawLimit)
			if err != nil {
				return Normalized{}, fmt.Errorf("invalid limit %q for %s: %w", rawLimit, name, err)
			}
			lq = q
		}
		// Kubernetes defaults an extended-resource request from its limit. For
		// regular resources, requests-only and limits-only remain distinct.
		if isExtended(name) && rq == nil {
			rq = lq
		}
		if rq != nil && lq != nil && rq.Cmp(*lq) > 0 {
			return Normalized{}, fmt.Errorf("request exceeds limit for %s", name)
		}
		if isExtended(name) && hasRequest && hasLimit && rq.Cmp(*lq) != 0 {
			return Normalized{}, fmt.Errorf("extended resource %s must have equal request and limit", name)
		}
		if rq != nil {
			requests[name] = rq.String()
		}
		if lq != nil {
			limits[name] = lq.String()
		}
	}
	return Normalized{Requests: requests, Limits: limits}, nil
}

func parseQuantity(raw string) (*resource.Quantity, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("quantity is empty")
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return nil, err
	}
	if q.Sign() < 0 {
		return nil, fmt.Errorf("quantity must be non-negative")
	}
	return &q, nil
}

func isExtended(name string) bool {
	return strings.Contains(name, "/") && !strings.HasPrefix(name, "kubernetes.io/")
}
