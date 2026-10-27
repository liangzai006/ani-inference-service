package inference

import (
	"crypto/sha256"
	"fmt"
)

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

func PullSecretName(tenantID, namespace, registry string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + namespace + "\x00" + registry))
	return fmt.Sprintf("ani-inference-pull-%x", sum[:16])
}
