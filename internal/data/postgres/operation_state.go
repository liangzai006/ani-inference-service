package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
)

// ErrOperationCAS means the operation was no longer at the expected
// tenant/generation/step (or is no longer the service's current operation).
// Callers should reload durable state rather than retrying the same write.
var ErrOperationCAS = errors.New("operation state CAS failed")

type OperationStepInput struct {
	TenantID, OperationID, Kind string
	LeaseToken                  string
	TargetGeneration            int64
	ExpectedPhase               inference.OperationPhase
	ExpectedStep                string
	NextPhase                   inference.OperationPhase
	NextStep                    string
	ErrorCode                   string
	ErrorMessage                string
}

func (r *Repository) AdvanceOperationStepCAS(ctx context.Context, in OperationStepInput) error {
	if r == nil || r.pool == nil {
		return errors.New("nil postgres operation repository")
	}
	tenant, err := parseUUID("tenant_id", in.TenantID, false)
	if err != nil {
		return err
	}
	operation, err := parseUUID("operation_id", in.OperationID, false)
	if err != nil {
		return err
	}
	if in.TargetGeneration < 1 {
		return fmt.Errorf("target generation must be positive")
	}
	lease, err := parseUUID("lease_token", in.LeaseToken, false)
	if err != nil {
		return err
	}
	if err := inference.ValidateOperationStepTransition(in.Kind, in.ExpectedPhase, in.ExpectedStep, in.NextPhase, in.NextStep); err != nil {
		return err
	}
	rows, err := New(r.pool).AdvanceOperationStepCAS(ctx, AdvanceOperationStepCASParams{
		TenantID: tenant, OperationID: operation, TargetGeneration: in.TargetGeneration,
		LeaseToken:    lease,
		ExpectedPhase: string(in.ExpectedPhase), ExpectedStep: in.ExpectedStep,
		NextPhase: string(in.NextPhase), NextStep: in.NextStep,
		ErrorCode: in.ErrorCode, ErrorMessage: in.ErrorMessage,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrOperationCAS
	}
	return nil
}

// RetryOperationStepCAS puts an active operation back into pending while
// retaining its current step.  The next worker must wait until retry_at and
// still pass the phase/step/aggregate fence before advancing it.
func (r *Repository) RetryOperationStepCAS(ctx context.Context, in OperationStepInput, retryAfter time.Duration) error {
	if r == nil || r.pool == nil {
		return errors.New("nil postgres operation repository")
	}
	if retryAfter < 0 {
		return fmt.Errorf("retry duration cannot be negative")
	}
	tenant, err := parseUUID("tenant_id", in.TenantID, false)
	if err != nil {
		return err
	}
	operation, err := parseUUID("operation_id", in.OperationID, false)
	if err != nil {
		return err
	}
	if in.Kind == "" || in.TargetGeneration < 1 || (in.ExpectedPhase != inference.OperationPending && in.ExpectedPhase != inference.OperationRunning) {
		return ErrOperationCAS
	}
	lease, err := parseUUID("lease_token", in.LeaseToken, false)
	if err != nil {
		return err
	}
	rows, err := New(r.pool).RetryOperationStepCAS(ctx, RetryOperationStepCASParams{
		TenantID: tenant, OperationID: operation, TargetGeneration: in.TargetGeneration,
		LeaseToken:    lease,
		ExpectedPhase: string(in.ExpectedPhase), ExpectedStep: in.ExpectedStep,
		RetrySeconds: retryAfter.Seconds(), ErrorCode: in.ErrorCode, ErrorMessage: in.ErrorMessage,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrOperationCAS
	}
	return nil
}
