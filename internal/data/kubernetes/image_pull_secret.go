package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const imagePullManagedBy = "ani-inference-image-pull"
const imageSpaceAnnotation = "ani.kubercloud.com/image-space-id"
const imageRegistryAnnotation = "ani.kubercloud.com/image-registry"
const imageCredentialGenerationAnnotation = "ani.kubercloud.com/image-credential-generation"

type PullMaterialSource interface {
	PullMaterial(context.Context, string) (*imagev1.GetTenantPullMaterialResponse, error)
}

// Only a temporary failure at the credential RPC boundary permits read-only
// runtime observation. Kubernetes writes and invalid material remain hard errors.
type pullMaterialUnavailableError struct{ cause error }

func (e *pullMaterialUnavailableError) Error() string {
	return "image pull material temporarily unavailable"
}

func (e *pullMaterialUnavailableError) Unwrap() error { return e.cause }

// SharedPullSecrets is the sole writer for Inference's tenant/namespace/registry
// credential scope. Its Secrets have no individual workload owner reference.
// Workload stop/delete never invokes this adapter or deletes its objects.
type SharedPullSecrets struct {
	Client   client.Client
	Material PullMaterialSource
	Now      func() time.Time
}

func (s *SharedPullSecrets) Ensure(ctx context.Context, spec RuntimeSpec) error {
	if spec.ImageSelection == nil {
		return nil
	}
	selection := spec.ImageSelection
	if err := validateImageProjection(spec); err != nil {
		return err
	}
	if s == nil || s.Client == nil || s.Material == nil {
		return errors.New("image pull synchronization is not configured")
	}
	material, err := s.Material.PullMaterial(ctx, spec.TenantID)
	if err != nil {
		if ctx.Err() == nil && (status.Code(err) == codes.Unavailable || status.Code(err) == codes.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded)) {
			return &pullMaterialUnavailableError{cause: err}
		}
		return fmt.Errorf("obtain image pull material: %w", err)
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if material == nil || material.GetTenantId() != spec.TenantID || material.GetSpaceId() == "" || material.GetRegistryAuthority() != selection.RegistryAuthority || material.GetUsername() == "" || material.GetSecret() == "" || material.GetGeneration() < 1 ||
		(material.GetExpiresAt() != nil && (!material.GetExpiresAt().IsValid() || !material.GetExpiresAt().AsTime().After(now))) {
		return errors.New("image pull material scope or validity mismatch")
	}
	// Credentials enter only this Secret payload, never the accepted spec,
	// object annotations, error strings or controller logs.
	dockerConfig, err := json.Marshal(map[string]any{"auths": map[string]any{material.GetRegistryAuthority(): map[string]string{
		"username": material.GetUsername(), "password": material.GetSecret(),
		"auth": base64.StdEncoding.EncodeToString([]byte(material.GetUsername() + ":" + material.GetSecret())),
	}}})
	if err != nil {
		return errors.New("encode image pull material")
	}
	desiredData := map[string][]byte{corev1.DockerConfigJsonKey: dockerConfig}
	var existing corev1.Secret
	key := types.NamespacedName{Namespace: spec.Namespace, Name: selection.PullSecretRef}
	err = s.Client.Get(ctx, key, &existing)
	if apierrors.IsNotFound(err) {
		return s.Client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name,
			Labels:      map[string]string{tenantIDLabel: spec.TenantID, "app.kubernetes.io/managed-by": imagePullManagedBy},
			Annotations: map[string]string{imageSpaceAnnotation: material.GetSpaceId(), imageRegistryAnnotation: material.GetRegistryAuthority(), imageCredentialGenerationAnnotation: strconv.FormatInt(material.GetGeneration(), 10)},
		}, Type: corev1.SecretTypeDockerConfigJson, Data: desiredData})
	}
	if err != nil {
		return err
	}
	if existing.Namespace != spec.Namespace || existing.Name != selection.PullSecretRef || existing.Type != corev1.SecretTypeDockerConfigJson ||
		existing.Labels[tenantIDLabel] != spec.TenantID || existing.Labels["app.kubernetes.io/managed-by"] != imagePullManagedBy ||
		existing.Annotations[imageSpaceAnnotation] != material.GetSpaceId() || existing.Annotations[imageRegistryAnnotation] != material.GetRegistryAuthority() ||
		len(existing.OwnerReferences) != 0 || existing.ResourceVersion == "" || !existing.DeletionTimestamp.IsZero() {
		return errors.New("image pull Secret ownership conflict")
	}
	oldGeneration, err := strconv.ParseInt(existing.Annotations[imageCredentialGenerationAnnotation], 10, 64)
	if err != nil || oldGeneration < 1 || oldGeneration > material.GetGeneration() {
		return errors.New("image pull Secret credential generation conflict")
	}
	if oldGeneration == material.GetGeneration() {
		if !reflect.DeepEqual(existing.Data, desiredData) {
			return errors.New("image pull Secret material differs at accepted credential generation")
		}
		return nil
	}
	if existing.Immutable != nil && *existing.Immutable {
		return errors.New("image pull Secret is immutable")
	}
	existing.Data = desiredData
	existing.Annotations[imageCredentialGenerationAnnotation] = strconv.FormatInt(material.GetGeneration(), 10)
	// Update carries the fetched resourceVersion; conflicts remain visible.
	return s.Client.Update(ctx, &existing)
}

func validateImageProjection(spec RuntimeSpec) error {
	s := spec.ImageSelection
	if s == nil {
		return nil
	}
	if spec.TenantID == "" || spec.Namespace == "" || s.ImageID == "" || (s.Scope != "tenant" && s.Scope != "platform") || s.RegistryAuthority == "" || s.OS == "" || s.Architecture == "" ||
		len(validation.IsValidLabelValue(s.OS)) != 0 || len(validation.IsValidLabelValue(s.Architecture)) != 0 ||
		len(validation.IsDNS1123Subdomain(s.PullSecretRef)) != 0 || s.PullSecretRef != inferencebiz.PullSecretName(spec.TenantID, spec.Namespace, s.RegistryAuthority) ||
		!strings.HasPrefix(spec.Image, s.RegistryAuthority+"/") || !strings.Contains(spec.Image, "@sha256:") {
		return errors.New("accepted image selection is incomplete or has mismatched pull scope")
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
