package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

var (
	ErrManagedGPUReleaseNotConfigured = errors.New("managed GPU release is not configured")
	ErrManagedGPUReleaseNotProven     = errors.New("managed GPU release was not proven")
)

type ManagedGPUReleaseCoordinator struct {
	Observer gpu.ReleaseObserver
	Contexts gpu.RefundContextStore
	Reporter gpu.RefundReporter
}

var _ inferencebiz.ManagedGPUReleasePort = (*ManagedGPUReleaseCoordinator)(nil)

func (c *ManagedGPUReleaseCoordinator) ReleaseManagedGPU(ctx context.Context, op inferencebiz.OperationContext) error {
	if c == nil || c.Observer == nil || c.Contexts == nil || c.Reporter == nil {
		return ErrManagedGPUReleaseNotConfigured
	}
	if !op.ManagedGPU || op.Kind != "delete" {
		return fmt.Errorf("%w: operation is not a managed GPU delete", ErrManagedGPUReleaseNotProven)
	}
	if strings.TrimSpace(op.ManagedGPUCreateOperationID) == "" {
		return fmt.Errorf("%w: original create operation is missing", ErrManagedGPUReleaseNotProven)
	}
	if len(op.ManagedGPUReleasePods) == 0 {
		return fmt.Errorf("%w: complete Pod UID scope is missing", ErrManagedGPUReleaseNotProven)
	}
	original, err := c.Contexts.LoadRefundContext(ctx, op.TenantID, op.ServiceID, op.ManagedGPUCreateOperationID)
	if err != nil {
		return fmt.Errorf("load managed GPU release context: %w", err)
	}
	if original.DeleteOperationID == "" || original.DeleteOperationID != op.ID {
		return fmt.Errorf("%w: persisted delete operation does not match", ErrManagedGPUReleaseNotProven)
	}
	clusterID := original.ClusterID
	if clusterID == "" && op.GPURequest != nil {
		clusterID = op.GPURequest.ClusterID
	}
	if clusterID == "" && op.GPUSnapshot != nil && op.GPUSnapshot.Request != nil {
		clusterID = op.GPUSnapshot.Request.ClusterID
	}
	if clusterID == "" {
		return fmt.Errorf("%w: GPU cluster identity is missing", ErrManagedGPUReleaseNotProven)
	}
	sealedAt := time.Now().UTC().Format(time.RFC3339Nano)
	observed, err := c.Observer.ObserveRelease(ctx, gpu.ReleaseObservationInput{TenantID: op.TenantID, RequestID: op.RequestID, Actor: op.Actor, ResourceID: op.ServiceID, CreateOperationID: op.ManagedGPUCreateOperationID, OwnerService: original.OwnerService, DeleteOperationID: op.ID, ClusterID: clusterID, SealedAt: sealedAt, Pods: append([]gpu.PodRef(nil), op.ManagedGPUReleasePods...)})
	if err != nil {
		return fmt.Errorf("observe managed GPU release: %w", err)
	}
	if observed.Finding != gpu.ReleaseFindingReleased || strings.TrimSpace(observed.EvidenceID) == "" || strings.TrimSpace(observed.EvidenceDigest) == "" {
		return fmt.Errorf("%w: accelerator finding=%q", ErrManagedGPUReleaseNotProven, observed.Finding)
	}
	releaseEventID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("ani-inference/managed-gpu-release/"+op.ID)).String()
	if err := c.Reporter.ReportResourceReleased(ctx, gpu.ReleaseNotification{ReleaseEventID: releaseEventID, CreateOperationID: op.ManagedGPUCreateOperationID, ResourceID: op.ServiceID, Reason: gpu.ResourceReleased}); err != nil {
		return fmt.Errorf("report managed GPU release: %w", err)
	}
	return nil
}
