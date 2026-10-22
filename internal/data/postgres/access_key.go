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
)

var _ accesskey.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, input accesskey.CreatePersistInput) (accesskey.PersistResult, error) {
	if r == nil || r.pool == nil {
		return accesskey.PersistResult{}, errors.New("nil postgres repository")
	}
	in := input.Record
	tenant, err := parseUUID("tenant_id", in.TenantID, false)
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	id, err := parseUUID("id", in.ID, false)
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	if in.CreateIdempotencyKey != "" {
		existing, lookupErr := q.GetInferenceAccessKeyByCreateIdempotency(ctx, GetInferenceAccessKeyByCreateIdempotencyParams{
			TenantID: tenant, CreateIdempotencyKey: textParam(in.CreateIdempotencyKey),
		})
		if lookupErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return accesskey.PersistResult{}, err
			}
			return accesskey.PersistResult{Record: accessKeyRecord(existing), Replayed: true}, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return accesskey.PersistResult{}, lookupErr
		}
	}
	created, err := q.InsertInferenceAccessKey(ctx, InsertInferenceAccessKeyParams{
		TenantID: tenant, ID: id, Name: in.Name, KeyPrefix: in.KeyPrefix,
		SecretHash: in.SecretHash, HashVersion: in.HashVersion,
		AllowedServiceIds: in.AllowedServiceIDs, AllowedModelNames: in.AllowedModelNames,
		RequestsPerSecond: in.RequestsPerSecond, RequestsPerMinute: in.RequestsPerMinute,
		RequestsPerHour: in.RequestsPerHour, ExpiresAt: timeParam(in.ExpiresAt),
		CreatedBy: in.CreatedBy, CreateIdempotencyKey: textParam(in.CreateIdempotencyKey),
	})
	if err != nil {
		if in.CreateIdempotencyKey != "" {
			existing, lookupErr := q.GetInferenceAccessKeyByCreateIdempotency(ctx, GetInferenceAccessKeyByCreateIdempotencyParams{
				TenantID: tenant, CreateIdempotencyKey: textParam(in.CreateIdempotencyKey),
			})
			if lookupErr == nil {
				if commitErr := tx.Commit(ctx); commitErr != nil {
					return accesskey.PersistResult{}, commitErr
				}
				return accesskey.PersistResult{Record: accessKeyRecord(existing), Replayed: true}, nil
			}
		}
		return accesskey.PersistResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"key_id": in.ID, "key_prefix": in.KeyPrefix})
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	if err := q.AppendInferenceAccessKeyAudit(ctx, AppendInferenceAccessKeyAuditParams{
		TenantID: tenant, EventID: pgUUID(uuid.New()), KeyID: id, EventType: "inference.access_key.created",
		Actor: in.CreatedBy, RequestID: input.RequestID, Payload: payload,
	}); err != nil {
		return accesskey.PersistResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return accesskey.PersistResult{}, err
	}
	return accesskey.PersistResult{Record: accessKeyRecord(created)}, nil
}

func (r *Repository) List(ctx context.Context, tenantID string, options accesskey.ListOptions) ([]accesskey.Record, int64, error) {
	if r == nil || r.pool == nil {
		return nil, 0, errors.New("nil postgres repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return nil, 0, err
	}
	q := New(r.pool)
	items, err := q.ListInferenceAccessKeys(ctx, ListInferenceAccessKeysParams{TenantID: tenant, Limit: int32(options.Limit), Offset: int32(options.Offset)})
	if err != nil {
		return nil, 0, err
	}
	total, err := q.CountInferenceAccessKeys(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	out := make([]accesskey.Record, 0, len(items))
	for _, item := range items {
		out = append(out, accessKeyRecord(item))
	}
	return out, total, nil
}

func (r *Repository) Get(ctx context.Context, tenantID, id string) (accesskey.Record, error) {
	if r == nil || r.pool == nil {
		return accesskey.Record{}, errors.New("nil postgres repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	keyID, err := parseUUID("id", id, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	row, err := New(r.pool).GetInferenceAccessKey(ctx, GetInferenceAccessKeyParams{TenantID: tenant, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	return accessKeyRecord(row), nil
}

func (r *Repository) GetByPrefix(ctx context.Context, prefix string) (accesskey.Record, error) {
	if r == nil || r.pool == nil {
		return accesskey.Record{}, errors.New("nil postgres repository")
	}
	row, err := New(r.pool).GetInferenceAccessKeyByPrefix(ctx, prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	return accessKeyRecord(row), nil
}

func (r *Repository) Revoke(ctx context.Context, tenantID, id, actor, requestID string) (accesskey.Record, error) {
	if r == nil || r.pool == nil {
		return accesskey.Record{}, errors.New("nil postgres repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	keyID, err := parseUUID("id", id, false)
	if err != nil {
		return accesskey.Record{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return accesskey.Record{}, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	current, err := q.LockInferenceAccessKey(ctx, LockInferenceAccessKeyParams{TenantID: tenant, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	if current.RevokedAt.Valid {
		if err := tx.Commit(ctx); err != nil {
			return accesskey.Record{}, err
		}
		return accessKeyRecord(current), nil
	}
	row, err := q.RevokeInferenceAccessKey(ctx, RevokeInferenceAccessKeyParams{TenantID: tenant, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.Record{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.Record{}, err
	}
	payload, err := json.Marshal(map[string]any{"key_id": id})
	if err != nil {
		return accesskey.Record{}, err
	}
	if err := q.AppendInferenceAccessKeyAudit(ctx, AppendInferenceAccessKeyAuditParams{
		TenantID: tenant, EventID: pgUUID(uuid.New()), KeyID: keyID, EventType: "inference.access_key.revoked",
		Actor: actor, RequestID: requestID, Payload: payload,
	}); err != nil {
		return accesskey.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return accesskey.Record{}, err
	}
	return accessKeyRecord(row), nil
}

func (r *Repository) Rotate(ctx context.Context, tenantID, id string, input accesskey.RotatePersistInput) (accesskey.PersistResult, error) {
	if r == nil || r.pool == nil {
		return accesskey.PersistResult{}, errors.New("nil postgres repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	keyID, err := parseUUID("id", id, false)
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	current, err := q.LockInferenceAccessKey(ctx, LockInferenceAccessKeyParams{TenantID: tenant, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return accesskey.PersistResult{}, accesskey.ErrNotFound
	}
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	if input.Idempotency != "" {
		existing, lookupErr := q.GetInferenceAccessKeyByRotationIdempotency(ctx, GetInferenceAccessKeyByRotationIdempotencyParams{TenantID: tenant, ID: keyID, RotationIdempotencyKey: textParam(input.Idempotency)})
		if lookupErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return accesskey.PersistResult{}, err
			}
			return accesskey.PersistResult{Record: accessKeyRecord(existing), Replayed: true}, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return accesskey.PersistResult{}, lookupErr
		}
	}
	if current.RevokedAt.Valid {
		return accesskey.PersistResult{}, accesskey.ErrAlreadyRevoked
	}
	rotated, err := q.RotateInferenceAccessKey(ctx, RotateInferenceAccessKeyParams{
		TenantID: tenant, ID: keyID, KeyPrefix: input.KeyPrefix, SecretHash: input.SecretHash,
		HashVersion: input.HashVersion, RotationIdempotencyKey: textParam(input.Idempotency),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, lookupErr := q.GetInferenceAccessKey(ctx, GetInferenceAccessKeyParams{TenantID: tenant, ID: keyID}); errors.Is(lookupErr, pgx.ErrNoRows) {
			return accesskey.PersistResult{}, accesskey.ErrNotFound
		}
		return accesskey.PersistResult{}, accesskey.ErrAlreadyRevoked
	}
	if err != nil {
		if input.Idempotency != "" {
			existing, lookupErr := q.GetInferenceAccessKeyByRotationIdempotency(ctx, GetInferenceAccessKeyByRotationIdempotencyParams{TenantID: tenant, ID: keyID, RotationIdempotencyKey: textParam(input.Idempotency)})
			if lookupErr == nil {
				if commitErr := tx.Commit(ctx); commitErr != nil {
					return accesskey.PersistResult{}, commitErr
				}
				return accesskey.PersistResult{Record: accessKeyRecord(existing), Replayed: true}, nil
			}
		}
		return accesskey.PersistResult{}, err
	}
	payload, err := json.Marshal(map[string]any{"key_id": id, "key_prefix": input.KeyPrefix})
	if err != nil {
		return accesskey.PersistResult{}, err
	}
	if err := q.AppendInferenceAccessKeyAudit(ctx, AppendInferenceAccessKeyAuditParams{
		TenantID: tenant, EventID: pgUUID(uuid.New()), KeyID: keyID, EventType: "inference.access_key.rotated",
		Actor: input.Actor, RequestID: input.RequestID, Payload: payload,
	}); err != nil {
		return accesskey.PersistResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return accesskey.PersistResult{}, err
	}
	return accesskey.PersistResult{Record: accessKeyRecord(rotated)}, nil
}

func (r *Repository) Consume(ctx context.Context, record accesskey.Record, now time.Time, cost int64) (bool, time.Duration, error) {
	if r == nil || r.pool == nil {
		return false, 0, errors.New("nil postgres repository")
	}
	tenant, err := parseUUID("tenant_id", record.TenantID, false)
	if err != nil {
		return false, 0, err
	}
	keyID, err := parseUUID("id", record.ID, false)
	if err != nil {
		return false, 0, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	now = now.UTC()
	for _, window := range []struct {
		name     string
		limit    int32
		duration time.Duration
	}{
		{name: "second", limit: record.RequestsPerSecond, duration: time.Second},
		{name: "minute", limit: record.RequestsPerMinute, duration: time.Minute},
		{name: "hour", limit: record.RequestsPerHour, duration: time.Hour},
	} {
		if window.limit <= 0 {
			continue
		}
		start := now.Truncate(window.duration)
		_, consumeErr := q.ConsumeInferenceAccessKeyRateBucket(ctx, ConsumeInferenceAccessKeyRateBucketParams{
			TenantID: tenant, KeyID: keyID, WindowName: window.name,
			WindowStart:  pgtype.Timestamptz{Time: start, Valid: true},
			RequestCount: cost, RequestCount_2: int64(window.limit),
		})
		if errors.Is(consumeErr, pgx.ErrNoRows) {
			if err := tx.Rollback(ctx); err != nil {
				return false, 0, err
			}
			retryAfter := start.Add(window.duration).Sub(now)
			if retryAfter < 0 {
				retryAfter = 0
			}
			return false, retryAfter, nil
		}
		if consumeErr != nil {
			return false, 0, consumeErr
		}
	}
	if err := q.MarkInferenceAccessKeyUsed(ctx, MarkInferenceAccessKeyUsedParams{TenantID: tenant, ID: keyID}); err != nil {
		return false, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	return true, 0, nil
}

func accessKeyRecord(row InferenceAccessKey) accesskey.Record {
	return accesskey.Record{
		TenantID: row.TenantID.String(), ID: row.ID.String(), Name: row.Name,
		KeyPrefix: row.KeyPrefix, SecretHash: row.SecretHash, HashVersion: row.HashVersion,
		AllowedServiceIDs: append([]string(nil), row.AllowedServiceIds...),
		AllowedModelNames: append([]string(nil), row.AllowedModelNames...),
		RequestsPerSecond: row.RequestsPerSecond, RequestsPerMinute: row.RequestsPerMinute,
		RequestsPerHour: row.RequestsPerHour, ExpiresAt: timePtr(row.ExpiresAt), RevokedAt: timePtr(row.RevokedAt),
		CreatedBy: row.CreatedBy, CreatedAt: timeValue(row.CreatedAt), LastUsedAt: timePtr(row.LastUsedAt),
		Version: row.Version, CreateIdempotencyKey: textValue(row.CreateIdempotencyKey), RotationIdempotency: textValue(row.RotationIdempotencyKey),
	}
}

func timeParam(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func timeValue(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func textParam(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
