-- name: InsertInferenceAccessKey :one
INSERT INTO inference_access_keys (
  tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, created_by, create_idempotency_key, secret_ciphertext, policy_id
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (tenant_id, create_idempotency_key) DO NOTHING
RETURNING tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id;

-- name: GetInferenceAccessKey :one
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys
WHERE tenant_id = $1 AND id = $2;

-- name: GetInferenceAccessKeyByPrefix :one
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys
WHERE key_prefix = $1;

-- name: GetInferenceAccessKeyByCreateIdempotency :one
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys
WHERE tenant_id = $1 AND create_idempotency_key = $2;

-- name: ListInferenceAccessKeys :many
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys AS key
WHERE key.tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.arg(served_model_name)::text = '' OR EXISTS (
    SELECT 1 FROM inference_access_policies AS policy
    WHERE policy.tenant_id = key.tenant_id AND policy.id = key.policy_id
      AND sqlc.arg(served_model_name)::text = ANY(policy.allowed_model_names)
  ))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountInferenceAccessKeys :one
SELECT count(*)::bigint
FROM inference_access_keys AS key
WHERE key.tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.arg(served_model_name)::text = '' OR EXISTS (
    SELECT 1 FROM inference_access_policies AS policy
    WHERE policy.tenant_id = key.tenant_id AND policy.id = key.policy_id
      AND sqlc.arg(served_model_name)::text = ANY(policy.allowed_model_names)
  ));

-- name: RevokeInferenceAccessKey :one
UPDATE inference_access_keys
SET revoked_at = COALESCE(revoked_at, now()), version = version + 1
WHERE tenant_id = $1 AND id = $2
RETURNING tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id;

-- name: RotateInferenceAccessKey :one
UPDATE inference_access_keys
SET key_prefix = $3, secret_hash = $4, hash_version = $5,
    revoked_at = NULL, rotation_idempotency_key = $6, secret_ciphertext = $7,
    version = version + 1
WHERE tenant_id = $1 AND id = $2 AND revoked_at IS NULL
RETURNING tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id;

-- name: GetInferenceAccessKeyByRotationIdempotency :one
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys
WHERE tenant_id = $1 AND id = $2 AND rotation_idempotency_key = $3;

-- name: LockInferenceAccessKey :one
SELECT tenant_id, id, name, key_prefix, secret_hash, hash_version,
  allowed_service_ids, allowed_model_names,
  requests_per_second, requests_per_minute, requests_per_hour,
  expires_at, revoked_at, created_by, created_at, last_used_at, version,
  create_idempotency_key, rotation_idempotency_key, secret_ciphertext, policy_id
FROM inference_access_keys
WHERE tenant_id = $1 AND id = $2
FOR UPDATE;

-- name: MarkInferenceAccessKeyUsed :exec
UPDATE inference_access_keys
SET last_used_at = now()
WHERE tenant_id = $1 AND id = $2;

-- name: AppendInferenceAccessKeyAudit :exec
INSERT INTO inference_access_key_audit_events (
  tenant_id, event_id, key_id, event_type, actor, request_id, payload
) VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ConsumeInferenceAccessKeyRateBucket :one
INSERT INTO inference_access_key_rate_buckets (
  tenant_id, key_id, window_name, window_start, request_count
) SELECT $1, $2, $3, $4, $5 WHERE $5::bigint <= sqlc.arg(max_count)::bigint
ON CONFLICT (tenant_id, key_id, window_name, window_start)
DO UPDATE SET request_count = inference_access_key_rate_buckets.request_count + EXCLUDED.request_count
WHERE inference_access_key_rate_buckets.request_count <= sqlc.arg(max_count)::bigint - EXCLUDED.request_count
RETURNING request_count;

-- name: ArchiveInferenceAccessKeySecret :execrows
UPDATE inference_access_keys
SET secret_ciphertext = $5
WHERE tenant_id = $1 AND id = $2 AND version = $3 AND secret_hash = $4
  AND secret_ciphertext IS NULL AND revoked_at IS NULL;
