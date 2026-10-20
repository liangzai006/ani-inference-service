package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

func (r *Repository) LoadManagedReleaseReport(ctx context.Context, original gpu.RefundContext) (gpu.ReleaseNotification, bool, error) {
	row, err := New(r.pool).GetManagedGPUCommand(ctx, GetManagedGPUCommandParams{TenantID: pgUUIDMust(original.TenantID), OperationID: pgUUIDMust(original.DeleteOperationID)})
	if err != nil {
		return gpu.ReleaseNotification{}, false, err
	}
	if len(row.ReleaseReport) == 0 {
		return gpu.ReleaseNotification{}, false, nil
	}
	var n gpu.ReleaseNotification
	if err := json.Unmarshal(row.ReleaseReport, &n); err != nil {
		return n, false, err
	}
	if row.ReleaseEvidenceID == "" || row.ReleaseEvidenceDigest == "" {
		return n, false, fmt.Errorf("release report has no durable evidence")
	}
	if err := gpu.ValidateRefund(n, original); err != nil {
		return n, false, err
	}
	return n, true, nil
}

func (r *Repository) SaveManagedReleaseReport(ctx context.Context, original gpu.RefundContext, evidenceID, evidenceDigest string, n gpu.ReleaseNotification, leaseToken string) error {
	if err := gpu.ValidateRefund(n, original); err != nil {
		return err
	}
	if evidenceID == "" || evidenceDigest == "" {
		return gpu.ErrInvalidRefund
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	// Both normal resource_work and DELETE-first close leases are checked
	// atomically by the SQL write before any report may leave this process.
	token, err := parseUUID("release lease", leaseToken, false)
	if err != nil {
		return err
	}
	rows, err := New(r.pool).SaveManagedReleaseReport(ctx, SaveManagedReleaseReportParams{TenantID: pgUUIDMust(original.TenantID), OperationID: pgUUIDMust(original.DeleteOperationID), EvidenceID: evidenceID, EvidenceDigest: evidenceDigest, ReleaseReport: payload, LeaseToken: token})
	if err != nil {
		return err
	}
	if rows != 1 {
		return inferencebiz.ErrGenerationConflict
	}
	return nil
}

// RunManagedCloses is invoked by the existing Loop on every pass. Its source
// is the managed command table, independent of service/work discovery.
func (r *Repository) RunManagedCloses(ctx context.Context, release inferencebiz.ManagedGPUReleasePort) error {
	tenants, err := New(r.pool).ListManagedCloseTenants(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return errors.Join(joined, ctx.Err())
		}
		token := pgUUID(uuid.New())
		row, err := New(r.pool).ClaimManagedClose(ctx, ClaimManagedCloseParams{TenantID: tenant, LeaseToken: token})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		original, e := r.LoadRefundContext(ctx, tenant.String(), row.ResourceID.String(), row.CreateOperationID.String())
		if e == nil {
			if original.ResourceClass == "cpu" {
				e = fmt.Errorf("CPU pending close lacks proven nonexecution")
			} else if release == nil {
				e = fmt.Errorf("managed GPU release is not configured")
			} else {
				raw, scopeErr := New(r.pool).GetManagedGPUReleaseScope(ctx, GetManagedGPUReleaseScopeParams{TenantID: tenant, ServiceID: row.ResourceID, OperationID: row.OperationID})
				if scopeErr != nil {
					e = scopeErr
				} else {
					seal, pods, scopeErr := decodeManagedGPUReleaseScope(raw)
					if scopeErr != nil {
						e = scopeErr
					} else if seal == "" {
						e = fmt.Errorf("managed close seal missing")
					} else {
						e = release.ReleaseManagedGPU(ctx, inferencebiz.OperationContext{TenantID: tenant.String(), ServiceID: row.ResourceID.String(), ID: row.OperationID.String(), Kind: "delete", Actor: row.Actor, RequestID: row.OperationID.String(), LeaseToken: token.String(), Managed: true, ManagedGPU: true, ManagedGPUCreateOperationID: row.CreateOperationID.String(), ManagedGPUReleaseSealedAt: seal, ManagedGPUReleasePods: pods})
					}
				}
			}
		}
		if e != nil {
			_, retryErr := New(r.pool).RetryManagedClose(ctx, RetryManagedCloseParams{TenantID: tenant, OperationID: row.OperationID, LeaseToken: token, CloseError: e.Error()})
			joined = errors.Join(joined, e, retryErr)
			continue
		}
		rows, e := New(r.pool).CompleteManagedClose(ctx, CompleteManagedCloseParams{TenantID: tenant, OperationID: row.OperationID, LeaseToken: token})
		if e == nil && rows != 1 {
			e = inferencebiz.ErrGenerationConflict
		}
		joined = errors.Join(joined, e)
	}
	return joined
}
