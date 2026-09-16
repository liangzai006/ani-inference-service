// Package reconcile defines the infrastructure-neutral reconciliation use
// case. A controller-runtime adapter supplies events; repositories supply PG.
package reconcile

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

var ErrStaleGeneration = errors.New("stale generation")

type Desired struct {
	TenantID, ServiceID string
	Generation          int64
}

// Observation is a runtime fact projection. Generation is mandatory for CAS;
// the remaining fields deliberately distinguish runtime readiness, model
// loading, publication and invocation health.
type Observation struct {
	Generation       int64
	RuntimePhase     string
	ReadyReplicas    int32
	ReadyGroups      int32
	ReadyWorkers     int32
	ModelReady       bool
	ModelReadyKnown  bool
	PublicationPhase string
	// Empty means no invocation fact was supplied; unknown explicitly records
	// a failed or inconclusive probe and must replace a previous healthy fact.
	InvocationHealth string
	RuntimeMode      string
	LWSUID           string
	Reason           string
	Objects          []RuntimeObject
}

type RuntimeObject struct {
	BindingGeneration       int64
	Kind, Namespace         string
	Name, UID               string
	ResourceVersion         string
	Role                    string
	ExpectedUID             string
	ExpectedResourceVersion string
	Missing                 bool
	Terminating             bool
}

type Repository interface {
	Current(context.Context, string, string) (Desired, error)
	SaveObservation(context.Context, string, string, int64, Observation) error
}

// LeasedRepository is the durable worker boundary. Implementations must
// validate tenant/service/generation and the PostgreSQL lease token in the
// same transaction that commits an observation.
type LeasedRepository interface {
	CurrentForWork(context.Context, work.Item) (Desired, error)
	SaveObservationForWork(context.Context, work.Item, Observation) error
}

var ErrLeaseRepositoryRequired = errors.New("lease-scoped reconcile repository is required")

type Runtime interface {
	Ensure(context.Context, Desired) (Observation, error)
}
type RuntimeFunc func(context.Context, Desired) (Observation, error)

func (f RuntimeFunc) Ensure(ctx context.Context, d Desired) (Observation, error) { return f(ctx, d) }

type Reconciler struct {
	Repository Repository
	Runtime    Runtime
}

func (r Reconciler) Reconcile(ctx context.Context, tenantID, serviceID string) error {
	desired, err := r.Repository.Current(ctx, tenantID, serviceID)
	if err != nil {
		return err
	}
	observation, err := r.Runtime.Ensure(ctx, desired)
	if err != nil {
		return err
	}
	if observation.Generation != desired.Generation {
		return ErrStaleGeneration
	}
	return r.Repository.SaveObservation(ctx, tenantID, serviceID, desired.Generation, observation)
}

// Execute runs one claimed durable work item. It is intentionally separate
// from the event-driven Reconcile method so controller-runtime events cannot
// bypass the PostgreSQL lease fence.
func (r Reconciler) Execute(ctx context.Context, item work.Item) (work.Result, error) {
	if r.Repository == nil || r.Runtime == nil {
		return work.Result{}, errors.New("reconcile repository and runtime are required")
	}
	repo, ok := r.Repository.(LeasedRepository)
	if !ok {
		return work.Result{}, ErrLeaseRepositoryRequired
	}
	if item.TenantID == "" || item.ServiceID == "" || item.Generation < 1 || item.LeaseToken == "" {
		return work.Result{}, ErrStaleGeneration
	}
	desired, err := repo.CurrentForWork(ctx, item)
	if err != nil {
		return work.Result{}, err
	}
	if desired.TenantID != item.TenantID || desired.ServiceID != item.ServiceID || desired.Generation != item.Generation {
		return work.Result{}, ErrStaleGeneration
	}
	observation, err := r.Runtime.Ensure(ctx, desired)
	if err != nil {
		return work.Result{}, err
	}
	if observation.Generation != item.Generation {
		return work.Result{}, ErrStaleGeneration
	}
	if err := repo.SaveObservationForWork(ctx, item, observation); err != nil {
		return work.Result{}, err
	}
	return work.Result{Generation: item.Generation}, nil
}
