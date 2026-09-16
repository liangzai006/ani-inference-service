package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OperationProjection is the operation portion of the durable status read.
// It is intentionally a value object so the projector cannot mutate the
// operation or advance its state as a side effect of a Kubernetes status write.
type OperationProjection struct {
	ID      string
	Kind    string
	Phase   string
	Step    string
	Message string
}

// StatusProjection is a read-only snapshot owned by PostgreSQL. AppliedGeneration
// is the generation that the durable reconciler has committed; it must not be
// populated from metadata.generation (the Kubernetes spec-change counter,
// distinct from metadata.resourceVersion).
type StatusProjection struct {
	ControlBinding        *RuntimeBinding
	AppliedGeneration     int64
	RuntimePhase          string
	ReadyReplicas         int32
	ModelReady            bool
	PublicationPhase      string
	PublicationEffective  bool
	InvocationHealth      string
	ObservedAt            *time.Time
	RuntimeStaleAfter     *time.Time
	PublicationObservedAt *time.Time
	InvocationObservedAt  *time.Time
	Operation             *OperationProjection
}

// StatusProjectionSource reads the durable projection without changing any
// business state. Implementations must read the primary database, enforce
// tenant scope, and return the latest control binding at/below desired generation
// in the same statement snapshot. A missing binding never authorizes a write.
type StatusProjectionSource interface {
	GetStatusProjection(context.Context, string, string) (StatusProjection, error)
}

// StatusProjector copies a PostgreSQL projection into CR status. APIReader is
// used for the base object so a stale informer cache cannot overwrite a newer
// status resourceVersion; Client.Status().Patch performs the status-only write.
type StatusProjector struct {
	Client    client.Client
	APIReader client.Reader
	Source    StatusProjectionSource
}

func (p *StatusProjector) Project(ctx context.Context, key client.ObjectKey) error {
	if p == nil || p.Client == nil {
		return errors.New("status projector client is nil")
	}
	if p.APIReader == nil {
		return errors.New("status projector API reader is nil")
	}
	if p.Source == nil {
		return errors.New("status projector source is nil")
	}

	var original *crdv1.InferenceService
	err := retry.OnError(retry.DefaultBackoff, apierrors.IsConflict, func() error {
		// Re-read both the API object and durable projection on every attempt.
		// A conflict means another writer advanced resourceVersion; reusing the
		// old merge base could otherwise overwrite a newer status snapshot.
		current := &crdv1.InferenceService{}
		if err := p.APIReader.Get(ctx, key, current); err != nil {
			return err
		}
		tenantID, serviceID, err := identityFromObject(current)
		if err != nil {
			return err
		}
		if original != nil && (current.UID != original.UID || tenantID != original.Labels[tenantIDLabel] || serviceID != original.Labels[serviceIDLabel]) {
			return errors.New("inference service identity changed during status projection")
		}
		if original == nil {
			original = current
		}
		projection, err := p.Source.GetStatusProjection(ctx, tenantID, serviceID)
		if err != nil {
			return err
		}
		binding := projection.ControlBinding
		if binding == nil || binding.Kind != "InferenceService" || binding.Role != "control" || binding.UID == "" ||
			binding.UID != string(current.UID) || binding.Namespace != current.Namespace || binding.Name != current.Name {
			return errors.New("inference service CR does not match its persisted control binding")
		}
		// The stored RV is from spec apply. Normal status writes advance it;
		// use the current APIReader RV below for optimistic concurrency.
		desired := current.DeepCopy()
		desired.Status = statusFromProjection(projection)
		if equalStatus(current.Status, desired.Status) {
			return nil
		}
		return p.Client.Status().Patch(ctx, desired, client.MergeFromWithOptions(current, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return fmt.Errorf("patch inference service status: %w", err)
	}
	return nil
}

func statusFromProjection(p StatusProjection) crdv1.InferenceServiceStatus {
	status := crdv1.InferenceServiceStatus{
		ObservedGeneration: p.AppliedGeneration,
		RuntimePhase:       p.RuntimePhase,
		ReadyReplicas:      p.ReadyReplicas,
		ModelReady:         p.ModelReady,
		PublicationPhase:   p.PublicationPhase,
		InvocationHealth:   p.InvocationHealth,
		Runtime: &crdv1.RuntimeStatus{
			Phase:         p.RuntimePhase,
			ReadyReplicas: p.ReadyReplicas,
			ModelReady:    p.ModelReady,
		},
		Publication: &crdv1.PublicationStatus{Phase: p.PublicationPhase, Effective: p.PublicationEffective},
		Invocation:  &crdv1.InvocationStatus{Health: p.InvocationHealth},
	}
	if p.ObservedAt != nil {
		t := metav1.NewTime(*p.ObservedAt)
		status.ObservedAt = &t
		status.Runtime.ObservedAt = &t
	}
	if p.RuntimeStaleAfter != nil {
		t := metav1.NewTime(*p.RuntimeStaleAfter)
		status.Runtime.StaleAfter = &t
	}
	if p.PublicationObservedAt != nil {
		t := metav1.NewTime(*p.PublicationObservedAt)
		status.Publication.ObservedAt = &t
	}
	if p.InvocationObservedAt != nil {
		t := metav1.NewTime(*p.InvocationObservedAt)
		status.Invocation.ObservedAt = &t
	}
	if p.Operation != nil {
		status.Operation = &crdv1.OperationStatus{ID: p.Operation.ID, Kind: p.Operation.Kind, Phase: p.Operation.Phase, Step: p.Operation.Step, Message: p.Operation.Message}
	}
	return status
}

// equalStatus avoids status update loops while keeping the comparison scoped
// to status only. DeepEqual handles nil-vs-empty optional fields consistently.
func equalStatus(a, b crdv1.InferenceServiceStatus) bool {
	return reflect.DeepEqual(a, b)
}
