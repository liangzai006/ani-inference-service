package kubernetes

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PullSecretEnsurer is the narrow Resource runtime port Inference uses now.
// Resource is the only writer of the shared tenant pull Secret.
type PullSecretEnsurer interface {
	EnsurePullSecret(ctx context.Context, tenant, namespace, namespaceUID, registry string) (*imagev1.EnsureTenantPullSecretResponse, error)
}

// Only a temporary failure at the Ensure RPC boundary permits read-only runtime
// observation. Anything else stays a hard error: there is no fallback to the
// removed SharedPullSecrets writer.
type pullSecretUnavailableError struct{ cause error }

func (e *pullSecretUnavailableError) Error() string {
	return "image pull Secret temporarily unavailable"
}

func (e *pullSecretUnavailableError) Unwrap() error { return e.cause }

// ResourcePullSecrets verifies the accepted selection and asks Resource to
// ensure the shared Secret in this namespace. It never reads credentials and
// never writes a Secret, so Inference holds no second writer.
type ResourcePullSecrets struct {
	Ensurer PullSecretEnsurer
	// Reader observes the actual Secret so the frozen reference can be proven
	// to exist before a workload is created. Read-only.
	Reader client.Reader
}

func (r *ResourcePullSecrets) Ensure(ctx context.Context, spec RuntimeSpec) error {
	if spec.ImageSelection == nil {
		return nil
	}
	if err := validateImageProjection(spec); err != nil {
		return err
	}
	if r == nil || r.Ensurer == nil || r.Reader == nil {
		return errors.New("image pull Secret synchronization is not configured")
	}
	selection := spec.ImageSelection
	ensured, err := r.Ensurer.EnsurePullSecret(ctx, spec.TenantID, spec.Namespace, spec.NamespaceUID, selection.RegistryAuthority)
	if err != nil {
		if ctx.Err() == nil && isTransientEnsureFailure(err) {
			return &pullSecretUnavailableError{cause: err}
		}
		return err
	}
	if ensured.GetSecretRef() != selection.PullSecretRef || ensured.GetRegistryAuthority() != selection.RegistryAuthority || ensured.GetGeneration() < 1 {
		return errors.New("image pull Secret receipt differs from the frozen reference")
	}
	// Confirm the object is visible with the frozen name before the workload is
	// created. A missing object is a hard error, not a reason to write one here.
	var secret corev1.Secret
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: spec.Namespace, Name: selection.PullSecretRef}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return errors.New("image pull Secret is not visible after ensure")
		}
		return fmt.Errorf("read ensured image pull Secret: %T", err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || len(secret.Data[corev1.DockerConfigJsonKey]) == 0 {
		return errors.New("ensured image pull Secret has unexpected content type")
	}
	return nil
}

// isTransientEnsureFailure classifies only availability/deadline failures so a
// definitive Resource denial is never mistaken for "not started yet".
func isTransientEnsureFailure(err error) bool {
	code := status.Code(err)
	return code == codes.Unavailable || code == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded)
}

func validateImageProjection(spec RuntimeSpec) error {
	s := spec.ImageSelection
	if s == nil {
		return nil
	}
	if spec.TenantID == "" || spec.Namespace == "" || spec.NamespaceUID == "" || s.ImageID == "" || (s.Scope != "tenant" && s.Scope != "platform") || s.RegistryAuthority == "" || s.OS == "" || s.Architecture == "" ||
		len(validation.IsValidLabelValue(s.OS)) != 0 || len(validation.IsValidLabelValue(s.Architecture)) != 0 ||
		len(validation.IsDNS1123Subdomain(s.PullSecretRef)) != 0 ||
		!strings.HasPrefix(spec.Image, s.RegistryAuthority+"/") || !strings.Contains(spec.Image, "@sha256:") {
		return errors.New("accepted image selection is incomplete or has mismatched pull scope")
	}
	// The frozen reference must be exactly the shared naming rule for this
	// tenant/cluster/namespace/registry scope. The service kind never appears.
	expected, err := inferencebiz.PullSecretName(spec.TenantID, spec.ClusterID, spec.Namespace, s.RegistryAuthority)
	if err != nil || expected != s.PullSecretRef {
		return errors.New("accepted pull Secret reference is not the shared tenant scope")
	}
	digest := spec.Image[strings.LastIndex(spec.Image, "@sha256:")+len("@sha256:"):]
	if _, err := hex.DecodeString(digest); err != nil || len(digest) != 64 || strings.ToLower(digest) != digest {
		return errors.New("accepted image reference must contain a fixed sha256 digest")
	}
	return nil
}

func applyImageProjection(spec RuntimeSpec, pod *corev1.PodSpec) error {
	if spec.ImageSelection == nil {
		return nil
	}
	if err := validateImageProjection(spec); err != nil {
		return err
	}
	if pod == nil {
		return errors.New("image projection pod is nil")
	}
	if pod.NodeSelector == nil {
		pod.NodeSelector = map[string]string{}
	}
	for key, value := range map[string]string{"kubernetes.io/os": spec.ImageSelection.OS, "kubernetes.io/arch": spec.ImageSelection.Architecture} {
		if existing, found := pod.NodeSelector[key]; found && existing != value {
			return errors.New("accepted image platform conflicts with scheduling placement")
		}
		pod.NodeSelector[key] = value
	}
	for _, ref := range pod.ImagePullSecrets {
		if ref.Name == spec.ImageSelection.PullSecretRef {
			return nil
		}
	}
	pod.ImagePullSecrets = append(pod.ImagePullSecrets, corev1.LocalObjectReference{Name: spec.ImageSelection.PullSecretRef})
	return nil
}
