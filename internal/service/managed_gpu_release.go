package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

var (
	ErrManagedGPUReleaseNotConfigured = errors.New("managed GPU release is not configured")
	ErrManagedGPUReleaseNotProven     = errors.New("managed GPU release was not proven")
)

// ManagedGPUReleaseCoordinator refunds a managed GPU resource once Inference
// itself has confirmed the runtime is gone. The proof is the persisted
// absence seal and Pod scope; Accelerator is not consulted on release.
type ManagedGPUReleaseCoordinator struct {
	Contexts gpu.RefundContextStore
	Reporter gpu.RefundReporter
}

var _ inferencebiz.ManagedGPUReleasePort = (*ManagedGPUReleaseCoordinator)(nil)

func (c *ManagedGPUReleaseCoordinator) ReleaseManagedGPU(ctx context.Context, op inferencebiz.OperationContext) error {
	if c == nil || c.Contexts == nil || c.Reporter == nil {
		return ErrManagedGPUReleaseNotConfigured
	}
	if !op.ManagedGPU || op.Kind != "delete" {
		return fmt.Errorf("%w: operation is not a managed GPU delete", ErrManagedGPUReleaseNotProven)
	}
	if strings.TrimSpace(op.ManagedGPUCreateOperationID) == "" {
		return fmt.Errorf("%w: original create operation is missing", ErrManagedGPUReleaseNotProven)
	}
	original, err := c.Contexts.LoadRefundContext(ctx, op.TenantID, op.ServiceID, op.ManagedGPUCreateOperationID)
	if err != nil {
		return fmt.Errorf("load managed GPU release context: %w", err)
	}
	if original.DeleteOperationID == "" || original.DeleteOperationID != op.ID {
		return fmt.Errorf("%w: persisted delete operation does not match", ErrManagedGPUReleaseNotProven)
	}
	if store, ok := c.Contexts.(gpu.ReleaseReportStore); ok {
		saved, exists, err := store.LoadManagedReleaseReport(ctx, original)
		if err != nil {
			return err
		}
		if exists {
			return c.Reporter.ReportResourceReleased(ctx, saved)
		}
	}
	// The seal is written only after runtime absence was observed. Without it
	// there is no evidence the GPU is free, so never fabricate one here.
	sealedAt := strings.TrimSpace(op.ManagedGPUReleaseSealedAt)
	if sealedAt == "" {
		return fmt.Errorf("%w: runtime absence was not sealed", ErrManagedGPUReleaseNotProven)
	}
	proof, err := gpu.NewReleaseProof(gpu.ReleaseProofInput{TenantID: op.TenantID, ResourceID: op.ServiceID, CreateOperationID: op.ManagedGPUCreateOperationID, DeleteOperationID: op.ID, SealedAt: sealedAt, Pods: append([]gpu.PodRef(nil), op.ManagedGPUReleasePods...)})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrManagedGPUReleaseNotProven, err)
	}
	releaseEventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("ani-inference/managed-gpu-release/"+op.ID)).String()
	notification := gpu.ReleaseNotification{ReleaseEventID: releaseEventID, CreateOperationID: op.ManagedGPUCreateOperationID, ResourceID: op.ServiceID, Reason: gpu.ResourceReleased}
	if store, ok := c.Contexts.(gpu.ReleaseReportStore); ok {
		if err := store.SaveManagedReleaseReport(ctx, original, proof.EvidenceID, proof.EvidenceDigest, notification, op.LeaseToken); err != nil {
			return fmt.Errorf("persist managed GPU release report: %w", err)
		}
	}
	if err := c.Reporter.ReportResourceReleased(ctx, notification); err != nil {
		return fmt.Errorf("report managed GPU release: %w", err)
	}
	return nil
}
