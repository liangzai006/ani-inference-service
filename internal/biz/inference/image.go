package inference

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrIncompletePullScope reports a pull Secret scope that cannot be derived.
var ErrIncompletePullScope = errors.New("image pull scope requires tenant, cluster, namespace and registry")

// ImageSelection is the accepted immutable catalog resolution. It never
// contains credentials. Retries and restarts reuse this persisted selection.
type ImageSelection struct {
	ImageID           string `json:"image_id"`
	Scope             string `json:"scope"`
	OS                string `json:"os"`
	Architecture      string `json:"architecture"`
	Variant           string `json:"variant,omitempty"`
	RegistryAuthority string `json:"registry_authority"`
	PullSecretRef     string `json:"pull_secret_ref"`
}

// PullSecretName mirrors Resource's single naming rule. The shared Secret is
// scoped by tenant, cluster, namespace and registry authority; the workload
// kind never participates. cluster_id must stay in the hash so the same tenant
// namespace name in two clusters cannot collide.
//
// The three services keep an identical implementation and are checked against
// shared test vectors; this repository cannot import Resource's internal code.
func PullSecretName(tenantID, clusterID, namespace, registry string) (string, error) {
	parts := []string{strings.ToLower(tenantID), strings.ToLower(clusterID), strings.ToLower(namespace), normalizeRegistryAuthority(registry)}
	for _, part := range parts {
		if part == "" {
			return "", ErrIncompletePullScope
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "ani-pull-" + hex.EncodeToString(sum[:16]), nil
}

// normalizeRegistryAuthority lowercases the authority and drops any scheme,
// path and the default registry port.
func normalizeRegistryAuthority(authority string) string {
	a := strings.TrimSpace(strings.ToLower(authority))
	if i := strings.Index(a, "://"); i >= 0 {
		a = a[i+3:]
	}
	if i := strings.IndexAny(a, "/?#"); i >= 0 {
		a = a[:i]
	}
	if strings.HasSuffix(a, ":443") {
		a = strings.TrimSuffix(a, ":443")
	}
	return a
}
