package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policy "github.com/liangzai006/ani-inference-service/internal/biz/accesspolicy"
)

const policyColumns = `id, name, allowed_model_names, requests_per_second, requests_per_minute, requests_per_hour, version`

type policyRow interface{ Scan(...any) error }

func scanPolicy(row policyRow, tenant string) (policy.Policy, error) {
	p := policy.Policy{TenantID: tenant}
	err := row.Scan(&p.ID, &p.Name, &p.AllowedModelNames, &p.RequestsPerSecond, &p.RequestsPerMinute, &p.RequestsPerHour, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, policy.ErrNotFound
	}
	return p, err
}
func policyAudit(ctx context.Context, tx pgx.Tx, p policy.Policy, event, actor, request string) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO inference_access_policy_audit_events(tenant_id,event_id,policy_id,event_type,actor,request_id,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, uuid.New(), p.ID, event, actor, request, payload)
	return err
}
func (r *Repository) CreateAccessPolicy(ctx context.Context, p policy.Policy, actor, request string) (policy.Policy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	p.ID = uuid.NewString()
	p, err = scanPolicy(tx.QueryRow(ctx, `INSERT INTO inference_access_policies(tenant_id,id,name,allowed_model_names,requests_per_second,requests_per_minute,requests_per_hour) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+policyColumns, p.TenantID, p.ID, p.Name, p.AllowedModelNames, p.RequestsPerSecond, p.RequestsPerMinute, p.RequestsPerHour), p.TenantID)
	if err != nil {
		return p, err
	}
	if err = policyAudit(ctx, tx, p, "created", actor, request); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
func (r *Repository) GetAccessPolicy(ctx context.Context, tenant, id string) (policy.Policy, error) {
	return scanPolicy(r.pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM inference_access_policies WHERE tenant_id=$1 AND id=$2`, tenant, id), tenant)
}
func (r *Repository) ListAccessPolicies(ctx context.Context, tenant string, limit, offset int) ([]policy.Policy, int64, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT `+policyColumns+` FROM inference_access_policies WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, tenant, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := []policy.Policy{}
	for rows.Next() {
		p, e := scanPolicy(rows, tenant)
		if e != nil {
			rows.Close()
			return nil, 0, e
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM inference_access_policies WHERE tenant_id=$1`, tenant).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, tx.Commit(ctx)
}
func (r *Repository) UpdateAccessPolicy(ctx context.Context, p policy.Policy, actor, request string) (policy.Policy, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	p, err = scanPolicy(tx.QueryRow(ctx, `UPDATE inference_access_policies SET name=$3,allowed_model_names=$4,requests_per_second=$5,requests_per_minute=$6,requests_per_hour=$7,version=version+1,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 RETURNING `+policyColumns, p.TenantID, p.ID, p.Name, p.AllowedModelNames, p.RequestsPerSecond, p.RequestsPerMinute, p.RequestsPerHour), p.TenantID)
	if err != nil {
		return p, err
	}
	if err = policyAudit(ctx, tx, p, "updated", actor, request); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
func (r *Repository) DeleteAccessPolicy(ctx context.Context, tenant, id, actor, request string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p, err := scanPolicy(tx.QueryRow(ctx, `DELETE FROM inference_access_policies WHERE tenant_id=$1 AND id=$2 RETURNING `+policyColumns, tenant, id), tenant)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23503" {
		return policy.ErrInUse
	}
	if err != nil {
		return err
	}
	if err = policyAudit(ctx, tx, p, "deleted", actor, request); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *Repository) BindAccessPolicy(ctx context.Context, tenant, keyID, policyID, actor, request string) (accesskey.Record, error) {
	tenantUUID, err := parseUUID("tenant_id", tenant, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	keyUUID, err := parseUUID("id", keyID, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return accesskey.Record{}, err
	}
	defer tx.Rollback(ctx)
	var current string
	err = tx.QueryRow(ctx, `SELECT id FROM inference_access_keys WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, keyID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM inference_access_policies WHERE tenant_id=$1 AND id=$2 FOR SHARE`, tenant, policyID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, policy.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE inference_access_keys SET policy_id=$3,version=version+1 WHERE tenant_id=$1 AND id=$2`, tenant, keyID, policyID)
	if err != nil {
		return accesskey.Record{}, err
	}
	payload, _ := json.Marshal(map[string]string{"policy_id": policyID})
	_, err = tx.Exec(ctx, `INSERT INTO inference_access_key_audit_events(tenant_id,event_id,key_id,event_type,actor,request_id,payload) VALUES($1,$2,$3,'inference.access_key.policy_bound',$4,$5,$6)`, tenant, uuid.New(), keyID, actor, request, payload)
	if err != nil {
		return accesskey.Record{}, err
	}
	row, err := New(tx).GetInferenceAccessKey(ctx, GetInferenceAccessKeyParams{TenantID: tenantUUID, ID: keyUUID})
	if err != nil {
		return accesskey.Record{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return accesskey.Record{}, err
	}
	return accessKeyRecord(row), nil
}
