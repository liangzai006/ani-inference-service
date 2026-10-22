package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policybiz "github.com/liangzai006/ani-inference-service/internal/biz/servicepolicy"
)

func readBoundPolicy(ctx context.Context, q *Queries, tenant, service pgtype.UUID) (policybiz.Policy, error) {
	row, err := q.GetBoundServicePolicy(ctx, GetBoundServicePolicyParams{TenantID: tenant, ID: service})
	if errors.Is(err, pgx.ErrNoRows) {
		return policybiz.Policy{}, policybiz.ErrNotFound
	}
	if err != nil {
		return policybiz.Policy{}, err
	}
	p := policybiz.Policy{ResourceID: row.ResourceID.String(), AllowedTenants: row.AllowedTenants, Version: row.Version, UpdatedAt: row.UpdatedAt.Time, RateLimits: map[string]policybiz.RateLimit{}}
	if err := json.Unmarshal(row.RateLimits, &p.RateLimits); err != nil {
		return policybiz.Policy{}, err
	}
	return p, nil
}
func (r *Repository) GetServicePolicy(ctx context.Context, tenant, service string) (policybiz.Policy, error) {
	t, err := parseUUID("tenant_id", tenant, false)
	if err != nil {
		return policybiz.Policy{}, err
	}
	id, err := parseUUID("resource_id", service, false)
	if err != nil {
		return policybiz.Policy{}, err
	}
	return readBoundPolicy(ctx, New(r.pool), t, id)
}
func (r *Repository) UpdateServicePolicy(ctx context.Context, tenant, actor, request string, policy policybiz.Policy) (policybiz.Policy, error) {
	t, err := parseUUID("tenant_id", tenant, false)
	if err != nil {
		return policybiz.Policy{}, err
	}
	id, err := parseUUID("resource_id", policy.ResourceID, false)
	if err != nil {
		return policybiz.Policy{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return policybiz.Policy{}, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	if _, err = q.LockPolicyService(ctx, LockPolicyServiceParams{TenantID: t, ID: id}); errors.Is(err, pgx.ErrNoRows) {
		return policybiz.Policy{}, policybiz.ErrNotFound
	} else if err != nil {
		return policybiz.Policy{}, err
	}
	if policy.RateLimits == nil {
		policy.RateLimits = map[string]policybiz.RateLimit{}
	}
	limits, err := json.Marshal(policy.RateLimits)
	if err != nil {
		return policybiz.Policy{}, err
	}
	if err = q.ReplaceBoundServicePolicy(ctx, ReplaceBoundServicePolicyParams{TenantID: t, ServiceID: id, AllowedTenants: policy.AllowedTenants, RateLimits: limits}); err != nil {
		return policybiz.Policy{}, err
	}
	out, err := readBoundPolicy(ctx, q, t, id)
	if err != nil {
		return policybiz.Policy{}, err
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return policybiz.Policy{}, err
	}
	if err = q.AppendServicePolicyAudit(ctx, AppendServicePolicyAuditParams{TenantID: t, ServiceID: id, EventID: pgtype.UUID{Bytes: uuid.New(), Valid: true}, Actor: actor, RequestID: request, Payload: payload}); err != nil {
		return policybiz.Policy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return policybiz.Policy{}, err
	}
	return out, nil
}
func lookupPublishedModel(ctx context.Context, q *Queries, model string) (ResolvePublishedModelRow, bool, error) {
	rows, err := q.ResolvePublishedModel(ctx, model)
	if err != nil {
		return ResolvePublishedModelRow{}, false, err
	}
	var resolved ResolvePublishedModelRow
	effective := false
	for i, row := range rows {
		if i > 0 && (row.TenantID != resolved.TenantID || row.ServiceID != resolved.ServiceID) {
			return ResolvePublishedModelRow{}, false, nil
		}
		resolved = row
		effective = effective || row.Effective
	}
	return resolved, effective, nil
}

// Resolve only accepts a unique effective publication; no caller-supplied
// service header can select the policy or authorize a different backend.
func (r *Repository) Resolve(ctx context.Context, model string) (string, error) {
	row, ok, err := lookupPublishedModel(ctx, New(r.pool), model)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}
	return row.ServiceID.String(), nil
}
func (r *Repository) ConsumeAccess(ctx context.Context, record accesskey.Record, service, model string, now time.Time, cost int64) (accesskey.CheckResult, error) {
	denied := accesskey.CheckResult{Record: record, DenialCode: "scope_denied", PolicyVersion: record.Version}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return denied, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	publication, ok, err := lookupPublishedModel(ctx, q, model)
	if err != nil {
		return denied, err
	}
	if !ok || publication.ServiceID.String() != service {
		return denied, nil
	}
	if _, err = q.LockPolicyServiceShared(ctx, LockPolicyServiceSharedParams{TenantID: publication.TenantID, ID: publication.ServiceID}); errors.Is(err, pgx.ErrNoRows) {
		return denied, nil
	} else if err != nil {
		return denied, err
	}
	// Re-read after taking the service lifecycle/policy fence.
	current, ok, err := lookupPublishedModel(ctx, q, model)
	if err != nil {
		return denied, err
	}
	if !ok || current.TenantID != publication.TenantID || current.ServiceID != publication.ServiceID {
		return denied, nil
	}
	policy, err := readBoundPolicy(ctx, q, publication.TenantID, publication.ServiceID)
	if err != nil {
		return denied, err
	}
	denied.PolicyVersion = policy.Version
	if !policy.Allows(record.TenantID) {
		denied.DenialCode = "policy_denied"
		return denied, nil
	}
	tenant, err := parseUUID("tenant_id", record.TenantID, false)
	if err != nil {
		return denied, err
	}
	key, err := parseUUID("key_id", record.ID, false)
	if err != nil {
		return denied, err
	}
	latest, err := q.LockAccessKeyForPolicy(ctx, LockAccessKeyForPolicyParams{TenantID: tenant, ID: key})
	if err != nil {
		return denied, err
	}
	if latest.Version != record.Version {
		denied.DenialCode = "invalid_key"
		return denied, nil
	}
	if latest.RevokedAt.Valid {
		denied.DenialCode = "revoked"
		return denied, nil
	}
	if latest.ExpiresAt.Valid && !latest.ExpiresAt.Time.After(now) {
		denied.DenialCode = "expired"
		return denied, nil
	}
	limits := policy.RateLimits[record.TenantID]
	// Service and key counters share one transaction: any denial rolls back all
	// counters, and changing policies never resets the current time window.
	for _, window := range []struct {
		name     string
		limit    int32
		duration time.Duration
	}{
		{"minute", limits.RequestsPerMinute, time.Minute}, {"hour", limits.RequestsPerHour, time.Hour},
	} {
		if window.limit <= 0 {
			continue
		}
		start := now.UTC().Truncate(window.duration)
		_, err = q.ConsumeServiceRateBucket(ctx, ConsumeServiceRateBucketParams{TenantID: publication.TenantID, ServiceID: publication.ServiceID, CallerTenantID: tenant, WindowName: window.name, WindowStart: pgtype.Timestamptz{Time: start, Valid: true}, Cost: cost, MaxCount: int64(window.limit)})
		if errors.Is(err, pgx.ErrNoRows) {
			denied.DenialCode = "rate_limited"
			denied.RetryAfter = start.Add(window.duration).Sub(now)
			return denied, nil
		}
		if err != nil {
			return denied, err
		}
	}
	allowed, retry, err := consumeKeyBuckets(ctx, q, tenant, key, record, now, cost)
	if err != nil {
		return denied, err
	}
	if !allowed {
		denied.DenialCode = "rate_limited"
		denied.RetryAfter = retry
		return denied, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return denied, err
	}
	return accesskey.CheckResult{Record: record, Allowed: true, PolicyVersion: policy.Version}, nil
}
