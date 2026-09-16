package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
)

// WorkStoreOptions controls durable lease and observation cadence.
type WorkStoreOptions struct {
	Owner                                      string
	LeaseSeconds, ObserveSeconds, RetrySeconds float64
}

type WorkStore struct {
	pool *pgxpool.Pool
	opts WorkStoreOptions
}

var _ work.MetricsSource = (*WorkStore)(nil)

// ServiceTarget is a tenant-scoped service discovered during startup
// recovery. Callers can use the generation to recreate a missing work row;
// discovery itself never advances a workflow.
type ServiceTarget struct {
	TenantID, ServiceID string
	Generation          int64
}

func NewWorkStore(pool *pgxpool.Pool, opts WorkStoreOptions) *WorkStore {
	if opts.LeaseSeconds <= 0 {
		opts.LeaseSeconds = 30
	}
	if opts.ObserveSeconds <= 0 {
		opts.ObserveSeconds = 30
	}
	if opts.RetrySeconds <= 0 {
		opts.RetrySeconds = 5
	}
	if opts.Owner == "" {
		opts.Owner = "inference-worker"
	}
	return &WorkStore{pool: pool, opts: opts}
}

// ListTenants is the explicit platform-worker discovery entry. Callers must
// pass each returned tenant to the tenant-scoped Claim/Scan methods; business
// queries never omit tenant_id predicates.
func (s *WorkStore) ListTenants(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("nil postgres work store")
	}
	rows, err := New(s.pool).ListTenantScopes(ctx)
	if err != nil {
		return nil, err
	}
	tenants := make([]string, 0, len(rows))
	for _, row := range rows {
		tenants = append(tenants, row.String())
	}
	return tenants, nil
}

// Recover rebuilds durable work and observation obligations on startup.
// It scans the durable service aggregate and upserts
// one tenant-scoped work row per undeleted service; the normal Claim path then
// decides which lease may execute it. No process-local queue is involved.
func (s *WorkStore) Recover(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("nil postgres work store")
	}
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if err := s.recoverTenant(ctx, tenant); err != nil {
			return err
		}
	}
	return nil
}

// Repair restores only missing resource_work rows from the durable service
// aggregate. Unlike Recover, it does not emit a notification for rows that
// already exist, so periodic full scans cannot create a hot loop or advance
// dirty_version on every service.
func (s *WorkStore) Repair(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("nil postgres work store")
	}
	tenants, err := s.ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		services, err := s.ScanUndeletedServices(ctx, tenant)
		if err != nil {
			return err
		}
		for _, service := range services {
			tenantID, err := tenantUUID(service.TenantID)
			if err != nil {
				return err
			}
			serviceUUID, err := workUUID("service_id", service.ServiceID)
			if err != nil {
				return err
			}
			if err := New(s.pool).EnsureResourceWork(ctx, EnsureResourceWorkParams{
				TenantID: tenantID, ServiceID: serviceUUID, DirtyVersion: service.Generation,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// MetricsSnapshot reads aggregate operational facts for low-cardinality
// metrics. It does not claim, notify, or otherwise mutate durable work.
func (s *WorkStore) MetricsSnapshot(ctx context.Context) (work.MetricsSnapshot, error) {
	if s == nil || s.pool == nil {
		return work.MetricsSnapshot{}, errors.New("nil postgres work store")
	}
	row, err := New(s.pool).GetWorkMetrics(ctx)
	if err != nil {
		return work.MetricsSnapshot{}, err
	}
	return work.MetricsSnapshot{DueWork: row.DueWork, OldestWorkAge: time.Duration(row.OldestWorkAgeSeconds * float64(time.Second)), StaleObservations: row.StaleObservations}, nil
}

func (s *WorkStore) recoverTenant(ctx context.Context, tenant string) error {
	services, err := s.ScanUndeletedServices(ctx, tenant)
	if err != nil {
		return err
	}
	for _, service := range services {
		if err := s.Notify(ctx, service.TenantID, service.ServiceID, service.Generation); err != nil {
			return err
		}
	}
	return nil
}

func workUUID(label, value string) (pgtype.UUID, error) {
	u, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%s: %w", label, err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
}

func tenantUUID(v string) (pgtype.UUID, error) { return workUUID("tenant_id", v) }

func (s *WorkStore) Claim(ctx context.Context, tenantID string) (work.Item, bool, error) {
	if s == nil || s.pool == nil {
		return work.Item{}, false, errors.New("nil postgres work store")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return work.Item{}, false, err
	}
	token := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	row, err := New(s.pool).ClaimResourceWork(ctx, ClaimResourceWorkParams{
		LeaseOwner: pgtype.Text{String: s.opts.Owner, Valid: true}, LeaseToken: token,
		LeaseSeconds: s.opts.LeaseSeconds, TenantID: tenant,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return work.Item{}, false, nil
	}
	if err != nil {
		return work.Item{}, false, err
	}
	return work.Item{TenantID: row.TenantID.String(), ServiceID: row.ServiceID.String(), Generation: row.DesiredGeneration, DirtyVersion: row.DirtyVersion, LeaseToken: row.LeaseToken.String()}, true, nil
}

// Notify durably records a wake-up for a service. dirty_version is a durable
// notification sequence: the SQL upsert increments it for every event,
// including duplicate or older-generation runtime events. The business
// generation remains in inference_services and is checked by Claim/Commit.
func (s *WorkStore) Notify(ctx context.Context, tenantID, serviceID string, dirtyVersion int64) error {
	if s == nil || s.pool == nil {
		return errors.New("nil postgres work store")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return err
	}
	service, err := workUUID("service_id", serviceID)
	if err != nil {
		return err
	}
	return New(s.pool).UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenant, ServiceID: service, DirtyVersion: dirtyVersion})
}

// ScanDue returns durable wake-up candidates for one tenant. It is safe to run
// at startup and periodically: the result is only a database snapshot, and a
// caller must still Claim each item before executing it.
func (s *WorkStore) ScanDue(ctx context.Context, tenantID string) ([]work.Item, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("nil postgres work store")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := New(s.pool).ListDueResourceWork(ctx, tenant)
	if err != nil {
		return nil, err
	}
	items := make([]work.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, work.Item{
			TenantID: row.TenantID.String(), ServiceID: row.ServiceID.String(),
			Generation: row.DesiredGeneration, DirtyVersion: row.DirtyVersion,
		})
	}
	return items, nil
}

// ScanUndeletedServices lists the durable service aggregate for one tenant.
// Startup recovery can compare this result with resource_work and call
// Notify for any missing row before running ScanDue.
func (s *WorkStore) ScanUndeletedServices(ctx context.Context, tenantID string) ([]ServiceTarget, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("nil postgres work store")
	}
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, err
	}
	rows, err := New(s.pool).ListUndeletedServices(ctx, tenant)
	if err != nil {
		return nil, err
	}
	services := make([]ServiceTarget, 0, len(rows))
	for _, row := range rows {
		services = append(services, ServiceTarget{
			TenantID: row.TenantID.String(), ServiceID: row.ID.String(),
			Generation: row.DesiredGeneration,
		})
	}
	return services, nil
}

func (s *WorkStore) Commit(ctx context.Context, item work.Item, result work.Result) error {
	if result.Generation != item.Generation {
		return work.ErrStaleGeneration
	}
	tenant, err := tenantUUID(item.TenantID)
	if err != nil {
		return err
	}
	service, err := workUUID("service_id", item.ServiceID)
	if err != nil {
		return err
	}
	token, err := workUUID("lease_token", item.LeaseToken)
	if err != nil {
		return err
	}
	rows, err := New(s.pool).CommitResourceWork(ctx, CommitResourceWorkParams{ClaimedDirtyVersion: item.DirtyVersion, ObserveSeconds: s.opts.ObserveSeconds, TenantID: tenant, ServiceID: service, DesiredGeneration: item.Generation, LeaseToken: token})
	if err != nil {
		return err
	}
	if rows != 1 {
		return work.ErrLeaseLost
	}
	return nil
}

func (s *WorkStore) Retry(ctx context.Context, item work.Item, cause error) error {
	tenant, err := tenantUUID(item.TenantID)
	if err != nil {
		return err
	}
	service, err := workUUID("service_id", item.ServiceID)
	if err != nil {
		return err
	}
	token, err := workUUID("lease_token", item.LeaseToken)
	if err != nil {
		return err
	}
	rows, err := New(s.pool).RetryResourceWork(ctx, RetryResourceWorkParams{RetrySeconds: s.opts.RetrySeconds, ErrorCode: workErrorCode(cause), TenantID: tenant, ServiceID: service, DesiredGeneration: item.Generation, LeaseToken: token})
	if err != nil {
		return err
	}
	if rows != 1 {
		return work.ErrLeaseLost
	}
	return nil
}

func workErrorCode(err error) string {
	if err == nil {
		return "retry"
	}
	if errors.Is(err, work.ErrStaleGeneration) {
		return "stale_generation"
	}
	code := strings.ToLower(err.Error())
	if len(code) > 120 {
		code = code[:120]
	}
	return code
}
