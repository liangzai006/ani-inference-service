package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
)

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
	if !ok || publication.ServiceID.String() != service || publication.TenantID.String() != record.TenantID {
		return denied, nil
	}
	if _, err = q.LockPolicyServiceShared(ctx, LockPolicyServiceSharedParams{TenantID: publication.TenantID, ID: publication.ServiceID}); errors.Is(err, pgx.ErrNoRows) {
		return denied, nil
	} else if err != nil {
		return denied, err
	}
	current, ok, err := lookupPublishedModel(ctx, q, model)
	if err != nil {
		return denied, err
	}
	if !ok || current.TenantID != publication.TenantID || current.ServiceID != publication.ServiceID {
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
	latest, err := q.LockInferenceAccessKey(ctx, LockInferenceAccessKeyParams{TenantID: tenant, ID: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return denied, nil
	}
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
	if !latest.PolicyID.Valid {
		return denied, nil
	}
	var models []string
	var qps, rpm, rph int32
	var version int64
	err = tx.QueryRow(ctx, `SELECT allowed_model_names,requests_per_second,requests_per_minute,requests_per_hour,version FROM inference_access_policies WHERE tenant_id=$1 AND id=$2 FOR SHARE`, tenant, latest.PolicyID).Scan(&models, &qps, &rpm, &rph, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied, nil
	}
	if err != nil {
		return denied, err
	}
	denied.PolicyVersion = version
	allowedModel := false
	for _, name := range models {
		if name == model {
			allowedModel = true
			break
		}
	}
	if !allowedModel {
		return denied, nil
	}
	record.RequestsPerSecond, record.RequestsPerMinute, record.RequestsPerHour = qps, rpm, rph
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
	return accesskey.CheckResult{Record: record, Allowed: true, PolicyVersion: version}, nil
}
