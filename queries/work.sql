-- name: ClaimResourceWork :one
WITH candidate AS (
    SELECT w.tenant_id, w.service_id, s.desired_generation
    FROM inference_resource_work w
    JOIN inference_services s ON s.tenant_id = w.tenant_id AND s.id = w.service_id
    WHERE w.tenant_id = sqlc.arg(tenant_id)
      AND s.deleted_at IS NULL
      AND (w.lease_until IS NULL OR w.lease_until <= clock_timestamp())
      AND (
        (w.next_run_at IS NOT NULL AND w.next_run_at <= clock_timestamp())
        OR (w.next_run_at IS NULL AND w.acknowledged_version < w.dirty_version)
      )
    ORDER BY w.next_run_at NULLS FIRST, w.updated_at, w.service_id
    LIMIT 1
    FOR UPDATE OF w SKIP LOCKED
)
UPDATE inference_resource_work w
SET lease_owner = sqlc.arg(lease_owner),
    lease_token = sqlc.arg(lease_token),
    lease_until = clock_timestamp() + sqlc.arg(lease_seconds)::double precision * interval '1 second',
    attempt = w.attempt + 1,
    updated_at = clock_timestamp()
FROM candidate c
WHERE w.tenant_id = c.tenant_id AND w.service_id = c.service_id
RETURNING w.tenant_id, w.service_id, w.dirty_version, c.desired_generation, w.lease_token;

-- name: CommitResourceWork :execrows
UPDATE inference_resource_work w
SET acknowledged_version = GREATEST(w.acknowledged_version, sqlc.arg(claimed_dirty_version)::bigint),
    next_run_at = CASE
        WHEN s.deleted_at IS NOT NULL THEN NULL
        WHEN w.dirty_version > sqlc.arg(claimed_dirty_version)::bigint THEN clock_timestamp()
        ELSE clock_timestamp() + sqlc.arg(observe_seconds)::double precision * interval '1 second'
    END,
    attempt = 0,
    lease_owner = NULL, lease_until = NULL, lease_token = NULL,
    last_error_code = '', updated_at = clock_timestamp()
FROM inference_services s
WHERE w.tenant_id = sqlc.arg(tenant_id) AND w.service_id = sqlc.arg(service_id)
  AND s.tenant_id = w.tenant_id AND s.id = w.service_id
  AND s.desired_generation = sqlc.arg(desired_generation)
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp()
  AND sqlc.arg(claimed_dirty_version)::bigint BETWEEN 0 AND w.dirty_version;

-- name: RetryResourceWork :execrows
UPDATE inference_resource_work w
SET next_run_at = clock_timestamp() + sqlc.arg(retry_seconds)::double precision * interval '1 second',
    lease_owner = NULL, lease_until = NULL, lease_token = NULL,
    last_error_code = sqlc.arg(error_code), updated_at = clock_timestamp()
FROM inference_services s
WHERE w.tenant_id = sqlc.arg(tenant_id) AND w.service_id = sqlc.arg(service_id)
  AND s.tenant_id = w.tenant_id AND s.id = w.service_id
  AND s.deleted_at IS NULL
  AND s.desired_generation = sqlc.arg(desired_generation)
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();

-- name: GetResourceWork :one
SELECT tenant_id, service_id, dirty_version, acknowledged_version, next_run_at,
       attempt, lease_owner, lease_until, lease_token, last_error_code, updated_at
FROM inference_resource_work
WHERE tenant_id = $1 AND service_id = $2;

-- name: GetLeasedDesired :one
-- A runtime worker must re-read the desired generation while holding its
-- PostgreSQL lease.  An event or stale in-memory item cannot authorize work.
SELECT s.tenant_id, s.id, s.desired_generation
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE s.tenant_id = $1 AND s.id = $2
  AND w.lease_token = $3
  AND w.lease_until > clock_timestamp();

-- name: ListDueResourceWork :many
-- This is a recovery/periodic scan.  The tenant predicate is mandatory so a
-- caller cannot accidentally turn a tenant worker into a cross-tenant queue.
SELECT w.tenant_id, w.service_id, w.dirty_version, s.desired_generation
FROM inference_resource_work w
JOIN inference_services s
  ON s.tenant_id = w.tenant_id AND s.id = w.service_id
WHERE w.tenant_id = sqlc.arg(tenant_id)
  AND s.deleted_at IS NULL
  AND (w.lease_until IS NULL OR w.lease_until <= clock_timestamp())
  AND (
    (w.next_run_at IS NOT NULL AND w.next_run_at <= clock_timestamp())
    OR (w.next_run_at IS NULL AND w.acknowledged_version < w.dirty_version)
  )
ORDER BY w.next_run_at NULLS FIRST, w.updated_at, w.service_id;

-- name: ListUndeletedServices :many
-- Startup recovery uses this list to repair a missing resource_work row before
-- the normal due scan.  It remains explicitly tenant-scoped.
SELECT tenant_id, id, desired_generation
FROM inference_services
WHERE tenant_id = sqlc.arg(tenant_id)
  AND deleted_at IS NULL
ORDER BY updated_at, id;

-- name: ListTenantScopes :many
-- Platform worker entry: this is the sole cross-tenant discovery query. Each
-- returned scope is passed to tenant-scoped Claim/Scan methods afterwards.
SELECT DISTINCT tenant_id
FROM inference_services
WHERE deleted_at IS NULL
ORDER BY tenant_id;
