package quota

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

// ErrReservationNotFound is the only lookup error that permits a new
// Reserve call. Any other provider error must be retried without guessing
// whether a remote reservation already exists.
var ErrReservationNotFound = errors.New("quota reservation not found")

// Reservation is the local projection of a reservation owned by the quota
// authority. Resource values use Kubernetes quantity strings.
type Reservation struct {
	TenantID, ServiceID, OperationID, Generation, ReservationID string
	// LeaseToken fences persistence of a provider result. It is never a
	// substitute for the provider's idempotency key; it only prevents an old
	// worker from writing after its PostgreSQL work lease expires.
	LeaseToken string
	// FenceOperationID is the current lifecycle operation. It differs from
	// OperationID when stop/delete releases an earlier reservation.
	FenceOperationID string
	// RequestedResources preserves the per-container requests/limits snapshot.
	// Aggregate replica/LWS accounting belongs to the quota adapter contract.
	RequestedResources resources.Spec
	// Demand is derived from the immutable inference spec for the operation.
	// It is passed to the authority but is not a second quota ledger.
	Demand        resources.Demand
	State         string
	LastErrorCode string
}

type Port interface {
	Reserve(context.Context, Reservation) (Reservation, error)
	Confirm(context.Context, Reservation) error
	Release(context.Context, Reservation) error
	Get(context.Context, string, string) (Reservation, error)
}
