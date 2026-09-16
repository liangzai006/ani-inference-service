package postgres

import (
	"context"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/admission"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

// Admission validates the immutable Inference spec before an operation asks
// external quota or runtime providers to act. It owns no IAM or quota policy;
// those remain separate authority boundaries.
type Admission struct {
	Source kube.DesiredRuntimeSource
}

func (a *Admission) Admit(ctx context.Context, op inferencebiz.OperationContext) error {
	if a == nil || a.Source == nil {
		return inferencebiz.ErrOperationProviderMissing
	}
	runtime, err := a.Source.CurrentRuntime(ctx, op.TenantID, op.ServiceID, op.TargetGeneration)
	if err != nil {
		return err
	}
	return admission.ValidateCreate(admission.CreateRequest{
		Replicas: runtime.Replicas, WorkerReplicas: runtime.WorkerReplicas,
		RuntimeMode: runtime.RuntimeMode,
		Resources:   admission.ResourceInput{Requests: runtime.Resources.Requests, Limits: runtime.Resources.Limits},
	})
}
