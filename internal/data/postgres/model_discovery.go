package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
)

// DiscoverAccess keeps the AK tenant authoritative and lists only models which
// the existing publication resolver would allow this key to invoke. Discovery
// consumes one request from the same per-key rate buckets as inference calls.
func (r *Repository) DiscoverAccess(ctx context.Context, record accesskey.Record, now time.Time) (accesskey.CheckResult, []accesskey.PublishedModel, error) {
	denied := accesskey.CheckResult{Record: record, DenialCode: "scope_denied"}
	tenant, err := parseUUID("tenant_id", record.TenantID, false)
	if err != nil {
		return denied, nil, err
	}
	key, err := parseUUID("key_id", record.ID, false)
	if err != nil {
		return denied, nil, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return denied, nil, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	latest, err := q.LockInferenceAccessKey(ctx, LockInferenceAccessKeyParams{TenantID: tenant, ID: key})
	if errors.Is(err, pgx.ErrNoRows) {
		denied.DenialCode = "invalid_key"
		return denied, nil, nil
	}
	if err != nil {
		return denied, nil, err
	}
	if latest.Version != record.Version {
		denied.DenialCode = "invalid_key"
		return denied, nil, nil
	}
	if latest.RevokedAt.Valid {
		denied.DenialCode = "revoked"
		return denied, nil, nil
	}
	if latest.ExpiresAt.Valid && !latest.ExpiresAt.Time.After(now) {
		denied.DenialCode = "expired"
		return denied, nil, nil
	}
	if !latest.PolicyID.Valid {
		return denied, nil, nil
	}
	var names []string
	err = tx.QueryRow(ctx, `SELECT allowed_model_names,requests_per_second,requests_per_minute,requests_per_hour,version
		FROM inference_access_policies WHERE tenant_id=$1 AND id=$2 FOR SHARE`, tenant, latest.PolicyID).
		Scan(&names, &record.RequestsPerSecond, &record.RequestsPerMinute, &record.RequestsPerHour, &denied.PolicyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied, nil, nil
	}
	if err != nil {
		return denied, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT sp.served_model_name, s.created_at
		FROM inference_publications p
		JOIN inference_services s ON s.tenant_id=p.tenant_id AND s.id=p.service_id
		JOIN inference_specs sp ON sp.tenant_id=p.tenant_id AND sp.service_id=p.service_id AND sp.generation=p.generation
		WHERE s.tenant_id=$1 AND sp.served_model_name=ANY($2::text[])
		AND s.deleted_at IS NULL AND s.desired_state='running' AND p.generation=s.desired_generation
		AND p.desired_phase='published' AND p.observed_phase='published'
		ORDER BY sp.served_model_name`, tenant, names)
	if err != nil {
		return denied, nil, err
	}
	var candidates []accesskey.PublishedModel
	for rows.Next() {
		var model accesskey.PublishedModel
		if err := rows.Scan(&model.ID, &model.Created); err != nil {
			rows.Close()
			return denied, nil, err
		}
		candidates = append(candidates, model)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return denied, nil, err
	}
	models := make([]accesskey.PublishedModel, 0, len(candidates))
	for _, model := range candidates {
		publication, effective, err := lookupPublishedModel(ctx, q, model.ID)
		if err != nil {
			return denied, nil, err
		}
		// This also excludes cross-tenant and stale-generation ambiguities.
		if effective && publication.TenantID == tenant {
			models = append(models, model)
		}
	}
	allowed, retry, err := consumeKeyBuckets(ctx, q, tenant, key, record, now, 1)
	if err != nil {
		return denied, nil, err
	}
	if !allowed {
		denied.DenialCode, denied.RetryAfter = "rate_limited", retry
		return denied, nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return denied, nil, err
	}
	return accesskey.CheckResult{Record: record, Allowed: true, PolicyVersion: denied.PolicyVersion}, models, nil
}
