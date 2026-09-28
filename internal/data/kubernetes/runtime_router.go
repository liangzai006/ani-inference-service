package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
)

// RuntimeRouter selects the runtime implementation from the persisted
// generation. The provider is a property of one service generation, so a
// process-wide environment switch cannot accidentally migrate existing
// services.
type RuntimeRouter struct {
	Source              DesiredRuntimeSource
	Deployment          inferencebiz.RuntimePort
	KServe              inferencebiz.RuntimePort
	DeploymentReconcile bizreconcile.Runtime
	KServeReconcile     bizreconcile.Runtime
}

var _ inferencebiz.RuntimePort = (*RuntimeRouter)(nil)
var _ bizreconcile.Runtime = (*RuntimeRouter)(nil)

func (r *RuntimeRouter) desired(ctx context.Context, op inferencebiz.OperationContext) (DesiredRuntime, error) {
	if r == nil || r.Source == nil {
		return DesiredRuntime{}, errors.New("runtime router source is nil")
	}
	if op.TenantID == "" || op.ServiceID == "" || op.TargetGeneration < 1 {
		return DesiredRuntime{}, bizreconcile.ErrStaleGeneration
	}
	return r.Source.CurrentRuntime(ctx, op.TenantID, op.ServiceID, op.TargetGeneration)
}

func (r *RuntimeRouter) choose(spec DesiredRuntime) (inferencebiz.RuntimePort, error) {
	provider := runtimeProviderForBindings(spec)
	switch provider {
	case "deployment":
		if r.Deployment == nil {
			return nil, errors.New("deployment runtime provider is not configured")
		}
		return r.Deployment, nil
	case "kserve":
		if r.KServe == nil {
			return nil, errors.New("KServe runtime provider is not configured")
		}
		return r.KServe, nil
	default:
		return nil, fmt.Errorf("unsupported runtime provider %q", provider)
	}
}

func (r *RuntimeRouter) chooseReconcile(spec DesiredRuntime) (bizreconcile.Runtime, error) {
	provider := runtimeProviderForBindings(spec)
	switch provider {
	case "deployment":
		if r.DeploymentReconcile == nil {
			return nil, errors.New("deployment reconcile provider is not configured")
		}
		return r.DeploymentReconcile, nil
	case "kserve":
		if r.KServeReconcile == nil {
			return nil, errors.New("KServe reconcile provider is not configured")
		}
		return r.KServeReconcile, nil
	default:
		return nil, fmt.Errorf("unsupported runtime provider %q", provider)
	}
}

// runtimeProviderForBindings prefers the persisted object kind during
// replacement/deletion. The target generation can still expose the previous
// generation's binding until absence is durably observed; using that binding
// is what makes a deployment→KServe update delete the old object correctly.
func runtimeProviderForBindings(spec DesiredRuntime) string {
	for _, binding := range spec.Bindings {
		if binding.Role != "" && binding.Role != "runtime" {
			continue
		}
		switch binding.Kind {
		case KServeInferenceServiceKind, KServeLLMInferenceServiceKind:
			return "kserve"
		case "Deployment", "LeaderWorkerSet":
			return "deployment"
		}
	}
	provider := strings.ToLower(strings.TrimSpace(spec.RuntimeProvider))
	if provider == "" {
		return "deployment"
	}
	return provider
}

func (r *RuntimeRouter) ApplyCR(ctx context.Context, op inferencebiz.OperationContext) error {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return err
	}
	return provider.ApplyCR(ctx, op)
}

func (r *RuntimeRouter) ApplyRuntime(ctx context.Context, op inferencebiz.OperationContext) error {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return err
	}
	return provider.ApplyRuntime(ctx, op)
}

func (r *RuntimeRouter) ObserveRuntime(ctx context.Context, op inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return inferencebiz.RuntimeObservation{}, err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return inferencebiz.RuntimeObservation{}, err
	}
	return provider.ObserveRuntime(ctx, op)
}

func (r *RuntimeRouter) DeleteRuntime(ctx context.Context, op inferencebiz.OperationContext) error {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return err
	}
	return provider.DeleteRuntime(ctx, op)
}

func (r *RuntimeRouter) ObserveAbsence(ctx context.Context, op inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return inferencebiz.RuntimeObservation{}, err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return inferencebiz.RuntimeObservation{}, err
	}
	return provider.ObserveAbsence(ctx, op)
}

func (r *RuntimeRouter) DeleteCR(ctx context.Context, op inferencebiz.OperationContext) error {
	spec, err := r.desired(ctx, op)
	if err != nil {
		return err
	}
	provider, err := r.choose(spec)
	if err != nil {
		return err
	}
	return provider.DeleteCR(ctx, op)
}

func (r *RuntimeRouter) Ensure(ctx context.Context, desired bizreconcile.Desired) (bizreconcile.Observation, error) {
	if r == nil || r.Source == nil {
		return bizreconcile.Observation{}, errors.New("runtime router source is nil")
	}
	spec, err := r.Source.CurrentRuntime(ctx, desired.TenantID, desired.ServiceID, desired.Generation)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	provider, err := r.chooseReconcile(spec)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	return provider.Ensure(ctx, desired)
}
