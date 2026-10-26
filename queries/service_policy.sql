-- name: GetBoundServicePolicy :one
SELECT s.id AS resource_id, COALESCE(p.allowed_tenants, ARRAY[s.tenant_id::text])::text[] AS allowed_tenants,
 COALESCE(p.rate_limits,'{}'::jsonb)::jsonb AS rate_limits,
 COALESCE(p.version,1)::bigint AS version, COALESCE(p.updated_at,s.created_at)::timestamptz AS updated_at
FROM inference_services s LEFT JOIN inference_service_policies p ON p.tenant_id=s.tenant_id AND p.service_id=s.id
WHERE s.tenant_id=$1 AND s.id=$2 AND s.deleted_at IS NULL AND s.desired_state<>'deleted';

-- name: LockPolicyService :one
SELECT id FROM inference_services WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL AND desired_state<>'deleted' FOR UPDATE;

-- name: LockPolicyServiceShared :one
SELECT id FROM inference_services WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL AND desired_state<>'deleted' FOR SHARE;

-- name: ReplaceBoundServicePolicy :exec
INSERT INTO inference_service_policies(tenant_id,service_id,allowed_tenants,rate_limits,version)
VALUES($1,$2,$3,$4,2)
ON CONFLICT(tenant_id,service_id) DO UPDATE SET allowed_tenants=EXCLUDED.allowed_tenants,
rate_limits=EXCLUDED.rate_limits, version=inference_service_policies.version+1, updated_at=clock_timestamp();

-- name: AppendServicePolicyAudit :exec
INSERT INTO inference_service_policy_audit_events(tenant_id,service_id,event_id,actor,request_id,payload)
VALUES($1,$2,$3,$4,$5,$6);

-- name: ResolvePublishedModel :many
-- Include transitional/stale publications in the ambiguity check. They can
-- still have a route in Envoy, even when they are no longer effective.
SELECT s.tenant_id, s.id AS service_id, p.generation,
 (s.deleted_at IS NULL AND s.desired_state='running' AND p.generation=s.desired_generation
  AND p.desired_phase='published' AND p.observed_phase='published')::boolean AS effective
FROM inference_publications p
JOIN inference_services s ON s.tenant_id=p.tenant_id AND s.id=p.service_id
JOIN inference_specs sp ON sp.tenant_id=p.tenant_id AND sp.service_id=p.service_id AND sp.generation=p.generation
WHERE sp.served_model_name=$1 AND (p.observed_phase<>'withdrawn' OR p.desired_phase<>'withdrawn');

-- name: ConsumeServiceRateBucket :one
INSERT INTO inference_service_rate_buckets(tenant_id,service_id,caller_tenant_id,window_name,window_start,request_count)
SELECT $1,$2,$3,$4,$5,sqlc.arg(cost)::bigint WHERE sqlc.arg(cost)::bigint <= sqlc.arg(max_count)::bigint
ON CONFLICT(tenant_id,service_id,caller_tenant_id,window_name,window_start)
DO UPDATE SET request_count=inference_service_rate_buckets.request_count+EXCLUDED.request_count
WHERE inference_service_rate_buckets.request_count <= sqlc.arg(max_count)::bigint-EXCLUDED.request_count
RETURNING request_count;

-- name: LockAccessKeyForPolicy :one
SELECT version, revoked_at, expires_at FROM inference_access_keys WHERE tenant_id=$1 AND id=$2 FOR UPDATE;
