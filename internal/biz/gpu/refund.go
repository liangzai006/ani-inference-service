package gpu

import (
	"context"
	"errors"
	"fmt"
)

var ErrInvalidRefund = errors.New("invalid managed GPU release")

// RefundContext is the owner-side immutable command context. Governance owns
// the original quota ledger; Inference stores no charge vector.
type RefundContext struct {
	TenantID                  string    `json:"tenant_id"`
	ResourceID                string    `json:"resource_id"`
	OriginalCreateOperationID string    `json:"original_create_operation_id"`
	DeleteOperationID         string    `json:"delete_operation_id"`
	OwnerService              string    `json:"owner_service"`
	ClusterID                 string    `json:"cluster_id"`
	Snapshot                  *Snapshot `json:"snapshot,omitempty"`
}

type RefundContextStore interface {
	LoadRefundContext(context.Context, string, string, string) (RefundContext, error)
}

type ReleaseReason string

const (
	ResourceReleased ReleaseReason = "RESOURCE_RELEASED"
	AbortedCleaned   ReleaseReason = "ABORTED_CLEANED"
)

// ReleaseNotification intentionally has exactly the four Governance release
// fields. Quota amounts and historical charges stay in Governance.
type ReleaseNotification struct {
	ReleaseEventID    string        `json:"release_event_id"`
	CreateOperationID string        `json:"create_operation_id"`
	ResourceID        string        `json:"resource_id"`
	Reason            ReleaseReason `json:"reason"`
}

type ReleaseReceipt struct{}

type RefundReporter interface {
	ReportResourceReleased(context.Context, ReleaseNotification) error
}

func ValidateRefund(n ReleaseNotification, original RefundContext) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalidRefund, message) }
	for name, value := range map[string]string{
		"release_event_id":    n.ReleaseEventID,
		"create_operation_id": n.CreateOperationID,
		"resource_id":         n.ResourceID,
	} {
		if !uuidPattern.MatchString(value) {
			return invalid(name + " must be a UUID")
		}
	}
	if n.CreateOperationID != original.OriginalCreateOperationID || n.ResourceID != original.ResourceID {
		return invalid("release identity mismatch")
	}
	if !uuidPattern.MatchString(original.DeleteOperationID) || original.DeleteOperationID == original.OriginalCreateOperationID {
		return invalid("persisted DELETE operation is required")
	}
	if n.Reason != ResourceReleased && n.Reason != AbortedCleaned {
		return invalid("release reason is required")
	}
	if original.OwnerService != "ani-inference" {
		return invalid("owner service mismatch")
	}
	return nil
}
