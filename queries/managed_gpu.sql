-- name: GetManagedGPUResource :one
SELECT * FROM inference_managed_gpu_resources
WHERE tenant_id = $1 AND resource_id = $2;

-- name: GetManagedGPUCommand :one
SELECT * FROM inference_managed_gpu_commands
WHERE tenant_id = $1 AND operation_id = $2;

-- name: LockManagedGPUKey :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: GetManagedGPUResourceForUpdate :one
SELECT * FROM inference_managed_gpu_resources
WHERE tenant_id = $1 AND resource_id = $2 FOR UPDATE;

-- name: GetManagedGPUState :one
SELECT EXISTS(SELECT 1 FROM inference_managed_gpu_resources AS managed_resource WHERE managed_resource.tenant_id=sqlc.arg(tenant_id) AND managed_resource.resource_id=sqlc.arg(resource_id)) AS managed,
       EXISTS(SELECT 1 FROM inference_managed_gpu_resources AS closing_resource WHERE closing_resource.tenant_id=sqlc.arg(tenant_id) AND closing_resource.resource_id=sqlc.arg(resource_id) AND closing_resource.closing) AS closing;

-- name: ManagedGPUServiceExists :one
SELECT EXISTS(SELECT 1 FROM inference_services WHERE tenant_id=$1 AND id=$2);

-- name: InsertManagedGPUResource :exec
INSERT INTO inference_managed_gpu_resources(tenant_id,resource_id,create_operation_id,owner_service,metering_version,original_context,resource_class,create_command_digest)
VALUES($1,$2,$3,$4,CASE WHEN sqlc.arg(resource_class)::text='cpu' THEN NULL ELSE 'gpu-metering-v1' END,$5,sqlc.arg(resource_class),sqlc.arg(create_command_digest));

-- name: SetManagedGPUClosing :exec
UPDATE inference_managed_gpu_resources
SET closing=TRUE,delete_operation_id=COALESCE(delete_operation_id,sqlc.arg(delete_operation_id)::uuid),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id) AND resource_id=sqlc.arg(resource_id);

-- name: SaveManagedGPUCreate :exec
UPDATE inference_managed_gpu_resources
SET business_payload=sqlc.arg(business_payload),business_payload_digest=sqlc.arg(business_payload_digest),
    create_request_hash=sqlc.arg(create_request_hash),create_payload=sqlc.arg(create_payload),updated_at=now()
WHERE tenant_id=sqlc.arg(tenant_id) AND resource_id=sqlc.arg(resource_id) AND create_payload IS NULL;

-- name: InsertManagedGPUCommand :exec
INSERT INTO inference_managed_gpu_commands(tenant_id,operation_id,resource_id,create_operation_id,kind,payload_hash,request_hash,command_payload,response_snapshot,durable_ack,execution_intent)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11);

-- name: GetManagedGPURefundContext :one
SELECT original_context,delete_operation_id FROM inference_managed_gpu_resources
WHERE tenant_id=$1 AND resource_id=$2 AND create_operation_id=$3;

-- name: UpsertManagedGPUReleaseScope :exec
INSERT INTO inference_managed_gpu_release_scopes
  (tenant_id, service_id, operation_id, pods)
VALUES ($1, $2, $3, sqlc.arg(pods)::jsonb)
ON CONFLICT (tenant_id, service_id, operation_id) DO UPDATE
SET pods = EXCLUDED.pods, updated_at = clock_timestamp();

-- name: GetManagedGPUReleaseScope :one
SELECT pods
FROM inference_managed_gpu_release_scopes
WHERE tenant_id = $1 AND service_id = $2 AND operation_id = $3;

-- name: SetManagedCloseState :exec
UPDATE inference_managed_gpu_commands SET actor=sqlc.arg(actor), close_state=sqlc.arg(close_state),
 completed_at=CASE WHEN sqlc.arg(close_state)::text='complete' THEN now() ELSE NULL END,
 close_error=sqlc.arg(close_error)
WHERE tenant_id=sqlc.arg(tenant_id) AND operation_id=sqlc.arg(operation_id) AND kind='delete';

-- name: ManagedExecutionFacts :one
SELECT EXISTS(SELECT 1 FROM inference_operations o WHERE o.tenant_id=sqlc.arg(tenant_id) AND o.service_id=sqlc.arg(resource_id)) OR
 EXISTS(SELECT 1 FROM inference_resource_work w WHERE w.tenant_id=sqlc.arg(tenant_id) AND w.service_id=sqlc.arg(resource_id)) OR
 EXISTS(SELECT 1 FROM inference_managed_gpu_resources r WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.resource_id=sqlc.arg(resource_id) AND r.create_payload IS NOT NULL) AS execution_unknown;

-- name: ListManagedCloseTenants :many
SELECT DISTINCT tenant_id FROM inference_managed_gpu_commands
WHERE kind='delete' AND close_state IN ('pending','unknown_execution') ORDER BY tenant_id;

-- name: ClaimManagedClose :one
WITH candidate AS (
 SELECT x.tenant_id,x.operation_id FROM inference_managed_gpu_commands x
 WHERE x.tenant_id=sqlc.arg(tenant_id) AND x.kind='delete' AND x.close_state='pending'
 AND x.retry_at<=now() AND (x.lease_until IS NULL OR x.lease_until<now())
 ORDER BY x.retry_at,x.operation_id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE inference_managed_gpu_commands c SET lease_token=sqlc.arg(lease_token),
 lease_until=now()+interval '30 seconds',attempt=c.attempt+1
FROM candidate p WHERE c.tenant_id=p.tenant_id AND c.operation_id=p.operation_id RETURNING c.*;

-- name: RetryManagedClose :execrows
UPDATE inference_managed_gpu_commands SET lease_token=NULL,lease_until=NULL,
 retry_at=now()+interval '5 seconds',close_error=sqlc.arg(close_error)
WHERE tenant_id=sqlc.arg(tenant_id) AND operation_id=sqlc.arg(operation_id) AND lease_token=sqlc.arg(lease_token) AND lease_until>now() AND close_state='pending';

-- name: CompleteManagedClose :execrows
UPDATE inference_managed_gpu_commands SET close_state='complete',completed_at=COALESCE(completed_at,now()),
 lease_token=NULL,lease_until=NULL,close_error=''
WHERE tenant_id=sqlc.arg(tenant_id) AND operation_id=sqlc.arg(operation_id)
AND (sqlc.narg(lease_token)::uuid IS NULL OR (lease_token=sqlc.narg(lease_token) AND lease_until>now()))
AND kind='delete';

-- name: SaveManagedReleaseReport :execrows
UPDATE inference_managed_gpu_commands c SET release_evidence_id=sqlc.arg(evidence_id),
 release_evidence_digest=sqlc.arg(evidence_digest),release_report=sqlc.arg(release_report)
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.operation_id=sqlc.arg(operation_id) AND c.kind='delete'
AND ((c.close_state='pending' AND c.lease_token=sqlc.arg(lease_token)::uuid AND c.lease_until>now())
 OR (c.close_state='' AND EXISTS(
  SELECT 1 FROM inference_resource_work w JOIN inference_services s
   ON s.tenant_id=w.tenant_id AND s.id=w.service_id
  WHERE w.tenant_id=c.tenant_id AND w.service_id=c.resource_id
   AND w.lease_token=sqlc.arg(lease_token)::uuid AND w.lease_until>now()
   AND s.current_operation_id=c.operation_id AND s.desired_state='deleted')))
AND EXISTS(SELECT 1 FROM inference_managed_gpu_resources r WHERE r.tenant_id=c.tenant_id AND r.resource_id=c.resource_id AND r.closing AND r.delete_operation_id=c.operation_id)
AND (c.release_report IS NULL OR (c.release_report=sqlc.arg(release_report) AND c.release_evidence_id=sqlc.arg(evidence_id) AND c.release_evidence_digest=sqlc.arg(evidence_digest)));
