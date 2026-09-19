-- name: GetService :one
SELECT tenant_id, id, name, desired_state, desired_generation, applied_generation, current_operation_id, deleted_at, created_at, updated_at
FROM inference_services
WHERE tenant_id = $1 AND id = $2;

-- name: GetStatusProjection :one
-- Read the complete status and binding snapshot in one statement, avoiding
-- mixed snapshots across separate queries. Keep the latest publication fact
-- at or below desired_generation so an older still-published route remains
-- visible while its effective bit is fenced to the exact generation.
SELECT s.applied_generation,
       COALESCE(control.generation, 0)::bigint AS control_generation,
       COALESCE(control.object_namespace, '') AS control_namespace,
       COALESCE(control.object_name, '') AS control_name,
       COALESCE(control.object_uid, '') AS control_uid,
       COALESCE(rt.runtime_phase, 'unknown') AS runtime_phase,
       COALESCE(rt.ready_replicas, 0)::int4 AS ready_replicas,
       CASE WHEN rt.model_ready_known AND rt.model_ready THEN TRUE ELSE FALSE END AS model_ready,
       COALESCE(pub.observed_phase, 'withdrawn') AS publication_phase,
       CASE WHEN pub.generation = s.desired_generation
                  AND pub.desired_phase = 'published'
                  AND pub.observed_phase = 'published'
            THEN TRUE ELSE FALSE END AS publication_effective,
       COALESCE(rt.invocation_health, 'unknown') AS invocation_health,
       rt.observed_at AS observed_at,
       rt.stale_after AS runtime_stale_after,
       pub.observed_at AS publication_observed_at,
       rt.observed_at AS invocation_observed_at,
       op.id AS operation_id,
       op.kind AS operation_kind,
       op.phase AS operation_phase,
       op.step AS operation_step,
       op.error_message AS operation_message
FROM inference_services s
LEFT JOIN LATERAL (
  SELECT b.generation, b.object_namespace, b.object_name, b.object_uid
  FROM inference_runtime_bindings b
  WHERE b.tenant_id = s.tenant_id AND b.service_id = s.id
    AND b.object_kind = 'InferenceService' AND b.role = 'control'
    AND b.generation <= s.desired_generation
  ORDER BY b.generation DESC
  LIMIT 1
) control ON TRUE
LEFT JOIN inference_runtime rt
  ON rt.tenant_id = s.tenant_id AND rt.service_id = s.id
LEFT JOIN LATERAL (
  SELECT p.generation, p.desired_phase, p.observed_phase, p.observed_at
  FROM inference_publications p
  WHERE p.tenant_id = s.tenant_id
    AND p.service_id = s.id
    AND p.generation <= s.desired_generation
  ORDER BY p.generation DESC, p.updated_at DESC
  LIMIT 1
) pub ON TRUE
LEFT JOIN inference_operations op
  ON op.tenant_id = s.tenant_id AND op.id = s.current_operation_id
WHERE s.tenant_id = sqlc.arg(tenant_id)
  AND s.id = sqlc.arg(service_id)
  AND s.deleted_at IS NULL;

-- name: ListServices :many
-- The cursor is the (created_at, id) key of the last row returned.  Both
-- predicates and the ordering are tenant-scoped and stable under inserts.
SELECT s.tenant_id, s.id, s.name, s.desired_state, s.desired_generation, s.applied_generation,
       s.created_at,
       sp.model_version_id AS model_version_id,
       COALESCE(sp.artifact_provider, '') AS artifact_provider,
       COALESCE(sp.artifact_ref, '') AS artifact_ref,
       COALESCE(sp.artifact_sha256, '') AS artifact_sha256,
       COALESCE(sp.image_ref, '') AS image_ref,
       COALESCE(sp.served_model_name, '') AS served_model_name,
       COALESCE(sp.engine_runtime, '') AS engine_runtime,
       COALESCE(sp.command_argv, '[]'::jsonb) AS command_argv,
       COALESCE(sp.resources, '{}'::jsonb) AS resources,
       COALESCE(sp.replicas, 0)::int4 AS replicas,
       COALESCE(sp.runtime_mode, 'deployment') AS runtime_mode,
       COALESCE(sp.worker_replicas, 1)::int4 AS worker_replicas,
       sp.endpoint_container_port, sp.endpoint_service_port,
       sp.endpoint_target_port, sp.endpoint_protocol,
       COALESCE(rt.runtime_phase, 'unknown') AS runtime_phase,
       COALESCE(pub.observed_phase, 'withdrawn') AS publication_phase,
       COALESCE(rt.invocation_health, 'unknown') AS invocation_health,
       rt.observed_at
FROM inference_services s
LEFT JOIN inference_specs sp
  ON sp.tenant_id = s.tenant_id AND sp.service_id = s.id
 AND sp.generation = s.desired_generation
LEFT JOIN inference_runtime rt
  ON rt.tenant_id = s.tenant_id AND rt.service_id = s.id
LEFT JOIN LATERAL (
  SELECT p.observed_phase FROM inference_publications p
  WHERE p.tenant_id = s.tenant_id AND p.service_id = s.id
    AND p.generation <= s.desired_generation
  ORDER BY p.generation DESC LIMIT 1
) pub ON TRUE
WHERE s.tenant_id = sqlc.arg(tenant_id)
  AND s.deleted_at IS NULL
  AND (sqlc.arg(cursor_created_at)::timestamptz IS NULL
       OR (s.created_at, s.id) <
          (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY s.created_at DESC, s.id DESC
LIMIT sqlc.arg(page_size);

-- name: GetServiceForUpdate :one
SELECT tenant_id, id, name, desired_state, desired_generation, applied_generation, current_operation_id, deleted_at, created_at, updated_at
FROM inference_services
WHERE tenant_id = $1 AND id = $2
FOR UPDATE;

-- name: GetSpec :one
SELECT * FROM inference_specs
WHERE tenant_id = $1 AND service_id = $2 AND generation = $3;

-- name: GetLatestSpec :one
SELECT * FROM inference_specs
WHERE tenant_id = $1 AND service_id = $2 AND generation <= $3
ORDER BY generation DESC
LIMIT 1;

-- name: CloneSpecGeneration :execrows
INSERT INTO inference_specs
  (tenant_id, id, service_id, generation, model_id, model_version_id,
   artifact_provider, artifact_ref, artifact_sha256, image_ref, served_model_name,
   engine_runtime, command_argv, resources, replicas, runtime_mode, worker_replicas, spec_json,
   endpoint_container_port, endpoint_service_port, endpoint_target_port, endpoint_protocol)
SELECT s.tenant_id, sqlc.arg(id), s.service_id, sqlc.arg(target_generation), s.model_id, s.model_version_id,
       s.artifact_provider, s.artifact_ref, s.artifact_sha256, s.image_ref, s.served_model_name,
       s.engine_runtime, s.command_argv, s.resources, s.replicas, s.runtime_mode, s.worker_replicas, s.spec_json,
       s.endpoint_container_port, s.endpoint_service_port, s.endpoint_target_port, s.endpoint_protocol
FROM inference_specs s
WHERE s.tenant_id = sqlc.arg(tenant_id) AND s.service_id = sqlc.arg(service_id)
  AND s.generation = sqlc.arg(source_generation);

-- name: CloneSpecWithRuntimeUpdate :execrows
INSERT INTO inference_specs
  (tenant_id, id, service_id, generation, model_id, model_version_id,
   artifact_provider, artifact_ref, artifact_sha256, image_ref, served_model_name,
   engine_runtime, command_argv, resources, replicas, runtime_mode, worker_replicas, spec_json,
   endpoint_container_port, endpoint_service_port, endpoint_target_port, endpoint_protocol)
SELECT s.tenant_id, sqlc.arg(id), s.service_id, sqlc.arg(target_generation), s.model_id,
       CASE WHEN sqlc.arg(model_version_id)::uuid IS NOT NULL THEN sqlc.arg(model_version_id)::uuid ELSE s.model_version_id END,
       CASE WHEN sqlc.arg(artifact_provider)::text <> '' THEN sqlc.arg(artifact_provider) ELSE s.artifact_provider END,
       CASE WHEN sqlc.arg(artifact_ref)::text <> '' THEN sqlc.arg(artifact_ref) ELSE s.artifact_ref END,
       CASE WHEN sqlc.arg(artifact_sha256)::text <> '' THEN sqlc.arg(artifact_sha256) ELSE s.artifact_sha256 END,
       CASE WHEN sqlc.arg(image_ref)::text <> '' THEN sqlc.arg(image_ref) ELSE s.image_ref END,
       CASE WHEN sqlc.arg(served_model_name)::text <> '' THEN sqlc.arg(served_model_name) ELSE s.served_model_name END,
       CASE WHEN sqlc.arg(engine_runtime)::text <> '' THEN sqlc.arg(engine_runtime) ELSE s.engine_runtime END,
       CASE WHEN CASE WHEN jsonb_typeof(sqlc.arg(command_argv)::jsonb) = 'array' THEN jsonb_array_length(sqlc.arg(command_argv)::jsonb) ELSE 0 END > 0 THEN sqlc.arg(command_argv) ELSE s.command_argv END,
       sqlc.arg(resources), sqlc.arg(replicas),
       sqlc.arg(runtime_mode), sqlc.arg(worker_replicas), s.spec_json,
       sqlc.arg(endpoint_container_port), sqlc.arg(endpoint_service_port), sqlc.arg(endpoint_target_port), sqlc.arg(endpoint_protocol)
FROM inference_specs s
WHERE s.tenant_id = sqlc.arg(tenant_id) AND s.service_id = sqlc.arg(service_id)
  AND s.generation = sqlc.arg(source_generation);

-- name: TransitionService :execrows
UPDATE inference_services
SET desired_state = sqlc.arg(desired_state), desired_generation = sqlc.arg(target_generation),
    current_operation_id = sqlc.arg(operation_id), updated_at = now()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(service_id)
  AND desired_generation = sqlc.arg(expected_generation) AND deleted_at IS NULL;

-- name: LockIdempotency :exec
-- Serialize same command identities before mutating the aggregate. Hash
-- collisions only serialize unrelated commands; tenant predicates remain on
-- every subsequent data access.
SELECT pg_advisory_xact_lock(hashtextextended(
    sqlc.arg(tenant_id)::text || ':' || sqlc.arg(method)::text || ':' || sqlc.arg(idempotency_key)::text, 0));

-- name: GetRuntime :one
-- Publication is read from its owner table, not an independently stale
-- runtime copy. Match ListServices so GET and LIST expose the same fact.
SELECT rt.tenant_id, rt.service_id, rt.generation, rt.runtime_phase, rt.ready_replicas, rt.model_ready, rt.model_ready_known,
       COALESCE(pub.observed_phase, 'withdrawn') AS publication_phase,
       rt.invocation_health, rt.observed_at, rt.stale_after, rt.reason,
       rt.runtime_mode, rt.ready_groups, rt.ready_workers, rt.lws_uid
FROM inference_runtime rt
JOIN inference_services s ON s.tenant_id = rt.tenant_id AND s.id = rt.service_id
LEFT JOIN LATERAL (
  SELECT p.observed_phase FROM inference_publications p
  WHERE p.tenant_id = rt.tenant_id AND p.service_id = rt.service_id
    AND p.generation <= s.desired_generation
  ORDER BY p.generation DESC LIMIT 1
) pub ON TRUE
WHERE rt.tenant_id = $1 AND rt.service_id = $2;

-- name: GetRuntimeBinding :one
SELECT tenant_id, service_id, generation, object_kind, object_namespace,
       object_name, object_uid, resource_version, role, observed_at
FROM inference_runtime_bindings
WHERE tenant_id = $1
  AND service_id = $2
  AND generation = $3
  AND object_kind = $4
  AND role = $5;

-- name: ListRuntimeBindings :many
SELECT tenant_id, service_id, generation, object_kind, object_namespace,
       object_name, object_uid, resource_version, role, observed_at
FROM inference_runtime_bindings
WHERE tenant_id = $1 AND service_id = $2 AND generation <= $3
ORDER BY object_kind, role;

-- name: ListCurrentRuntimeBindings :many
-- Lifecycle commands advance desired generation before removing the last
-- applied runtime. Keep its latest recorded identity until removal is proven.
SELECT DISTINCT ON (object_kind, role)
       tenant_id, service_id, generation, object_kind, object_namespace,
       object_name, object_uid, resource_version, role, observed_at
FROM inference_runtime_bindings
WHERE tenant_id = $1 AND service_id = $2 AND generation <= $3
ORDER BY object_kind, role, generation DESC;

-- name: UpsertRuntimeBindingCAS :execrows
-- An insert creates the binding.  An update is accepted only when both
-- fencing values still match the values the caller read.  Kubernetes
-- resourceVersion is opaque and is compared only for equality. An exact
-- replay of an initial insert is a no-op that refreshes observed_at.
INSERT INTO inference_runtime_bindings
  (tenant_id, service_id, generation, object_kind, object_namespace,
   object_name, object_uid, resource_version, role)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (tenant_id, service_id, generation, object_kind, role) DO UPDATE
SET object_namespace = EXCLUDED.object_namespace,
    object_name = EXCLUDED.object_name,
    object_uid = EXCLUDED.object_uid,
    resource_version = EXCLUDED.resource_version,
    observed_at = clock_timestamp()
WHERE inference_runtime_bindings.object_uid = EXCLUDED.object_uid
  AND inference_runtime_bindings.object_namespace = EXCLUDED.object_namespace
  AND inference_runtime_bindings.object_name = EXCLUDED.object_name
  AND ((inference_runtime_bindings.object_uid = sqlc.arg(expected_uid)
        AND inference_runtime_bindings.resource_version = sqlc.arg(expected_resource_version))
       OR (sqlc.arg(expected_uid) = ''
           AND sqlc.arg(expected_resource_version) = ''
           AND inference_runtime_bindings.resource_version = EXCLUDED.resource_version));

-- name: DeleteRuntimeBindingCAS :execrows
DELETE FROM inference_runtime_bindings
WHERE tenant_id = $1
  AND service_id = $2
  AND generation = $3
  AND object_kind = $4
  AND role = $5
  AND object_uid = $6
  AND resource_version = $7;

-- name: UpdateObservationCAS :execrows
UPDATE inference_runtime r
SET generation = sqlc.arg(generation), runtime_mode = sqlc.arg(runtime_mode),
    ready_groups = sqlc.arg(ready_groups), ready_workers = sqlc.arg(ready_workers),
    lws_uid = sqlc.arg(lws_uid), runtime_phase = sqlc.arg(runtime_phase),
    ready_replicas = sqlc.arg(ready_replicas), model_ready = sqlc.arg(model_ready),
    publication_phase = sqlc.arg(publication_phase),
    invocation_health = sqlc.arg(invocation_health), observed_at = now(),
    stale_after = now() + interval '2 minutes',
    updated_at = now(), reason = sqlc.arg(reason)
FROM inference_services s
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.service_id = sqlc.arg(service_id)
  AND s.tenant_id = r.tenant_id AND s.id = r.service_id
  AND s.desired_generation = sqlc.arg(generation)
  AND r.generation <= sqlc.arg(generation);

-- name: UpdateObservationForWork :execrows
-- Observation writes are fenced by the same durable work lease used for the
-- runtime call. A late worker cannot overwrite a newer claim or generation.
UPDATE inference_runtime r
SET generation = sqlc.arg(generation), runtime_mode = sqlc.arg(runtime_mode),
    ready_groups = sqlc.arg(ready_groups), ready_workers = sqlc.arg(ready_workers),
    lws_uid = sqlc.arg(lws_uid), runtime_phase = sqlc.arg(runtime_phase),
    ready_replicas = sqlc.arg(ready_replicas), model_ready = sqlc.arg(model_ready),
    publication_phase = sqlc.arg(publication_phase),
    invocation_health = sqlc.arg(invocation_health), observed_at = now(),
    stale_after = now() + interval '2 minutes',
    updated_at = now(), reason = sqlc.arg(reason)
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.service_id = sqlc.arg(service_id)
  AND s.tenant_id = r.tenant_id AND s.id = r.service_id
  AND s.desired_generation = sqlc.arg(generation)
  AND r.generation <= sqlc.arg(generation)
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();

-- name: UpdateModelObservationForWork :execrows
-- Model materialization updates only its own fact while preserving runtime,
-- publication, and invocation fields written by other observers.
UPDATE inference_runtime r
SET model_ready = sqlc.arg(model_ready), observed_at = now(),
    model_ready_known = TRUE,
    stale_after = now() + interval '2 minutes',
    updated_at = now(), reason = sqlc.arg(reason)
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.service_id = sqlc.arg(service_id)
  AND s.tenant_id = r.tenant_id AND s.id = r.service_id
  AND s.desired_generation = sqlc.arg(generation)
  AND r.generation <= sqlc.arg(generation)
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();

-- name: MarkModelMaterializingForWork :execrows
-- Clear a previous generation's model-ready fact before a new materialization
-- attempt so stale readiness cannot authorize publication.
UPDATE inference_runtime r
SET model_ready = FALSE, model_ready_known = FALSE, observed_at = now(),
    stale_after = now() + interval '2 minutes',
    updated_at = now(), reason = 'model materialization pending'
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.service_id = sqlc.arg(service_id)
  AND s.tenant_id = r.tenant_id AND s.id = r.service_id
  AND s.desired_generation = sqlc.arg(generation)
  AND r.generation <= sqlc.arg(generation)
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();

-- name: AppendObservationEvent :exec
-- Observation events are append-only history. The caller supplies a fresh
-- event_id for every committed observation; this row is never updated.
INSERT INTO inference_observation_events
  (tenant_id, event_id, service_id, generation, source_kind,
   source_uid, source_resource_version, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: AppendAuditEvent :exec
INSERT INTO inference_audit_events
  (tenant_id, event_id, service_id, operation_id, generation, event_type, actor, request_id, before_state, after_state, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (tenant_id, event_id) DO NOTHING;

-- name: InsertService :exec
INSERT INTO inference_services
  (tenant_id, id, name, desired_state, desired_generation, current_operation_id)
VALUES ($1, $2, $3, $4, $5, NULL);

-- name: MarkServiceDeleted :execrows
UPDATE inference_services
SET deleted_at = COALESCE(deleted_at, clock_timestamp()), updated_at = clock_timestamp()
WHERE tenant_id = $1 AND id = $2 AND current_operation_id = $3
  AND desired_state = 'deleted' AND desired_generation = $4;

-- name: MarkServiceAppliedGeneration :execrows
-- The observation CAS has already fenced the lease/generation in the same
-- transaction. This projection records the latest generation observed after
-- runtime application without replacing desired_generation.
UPDATE inference_services
SET applied_generation = GREATEST(applied_generation, sqlc.arg(generation)),
    updated_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(service_id)
  AND desired_generation = sqlc.arg(generation)
  AND deleted_at IS NULL;

-- name: InsertSpec :exec
INSERT INTO inference_specs
  (tenant_id, id, service_id, generation, model_id, model_version_id,
   artifact_provider, artifact_ref, artifact_sha256, image_ref, served_model_name,
   engine_runtime, command_argv, resources, replicas, runtime_mode, worker_replicas, spec_json,
   endpoint_container_port, endpoint_service_port, endpoint_target_port, endpoint_protocol)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
        $19, $20, $21, $22);

-- name: InsertOperation :exec
INSERT INTO inference_operations
  (tenant_id, id, service_id, kind, phase, step, target_generation, request_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: SetCurrentOperation :exec
UPDATE inference_services
SET current_operation_id = $3, updated_at = now()
WHERE tenant_id = $1 AND id = $2;

-- name: UpsertResourceWork :exec
INSERT INTO inference_resource_work (tenant_id, service_id, dirty_version, acknowledged_version)
VALUES ($1, $2, $3, 0)
ON CONFLICT (tenant_id, service_id) DO UPDATE
-- dirty_version is a durable notification sequence, not the business
-- generation. Every event (including a same/older generation status event)
-- increments it so a notification received while a worker is running cannot
-- be hidden by that worker's acknowledgement. The service generation remains
-- read from inference_services by Claim/Commit CAS.
SET dirty_version = GREATEST(inference_resource_work.dirty_version + 1, EXCLUDED.dirty_version),
    next_run_at = clock_timestamp(),
    updated_at = clock_timestamp();

-- name: EnsureResourceWork :exec
-- Startup/periodic repair must not turn a complete service scan into a
-- notification storm. Existing rows retain their durable sequence and
-- schedule; only a missing row is recreated and made immediately due.
INSERT INTO inference_resource_work (tenant_id, service_id, dirty_version, acknowledged_version)
VALUES ($1, $2, $3, 0)
ON CONFLICT (tenant_id, service_id) DO NOTHING;

-- name: GetWorkMetrics :one
-- Operational gauges are intentionally aggregate and contain no tenant or
-- resource identifiers. The caller uses this only for low-cardinality metrics.
SELECT
  (SELECT count(*)::bigint
   FROM inference_resource_work
   WHERE next_run_at IS NOT NULL AND next_run_at <= clock_timestamp()) AS due_work,
  COALESCE((SELECT EXTRACT(EPOCH FROM (clock_timestamp() - min(next_run_at)))
            FROM inference_resource_work
            WHERE next_run_at IS NOT NULL AND next_run_at <= clock_timestamp()), 0)::double precision AS oldest_work_age_seconds,
  (SELECT count(*)::bigint
   FROM inference_runtime rt
   JOIN inference_services s
     ON s.tenant_id = rt.tenant_id AND s.id = rt.service_id
   WHERE s.deleted_at IS NULL
     AND rt.stale_after IS NOT NULL AND rt.stale_after <= clock_timestamp()) AS stale_observations;

-- name: InsertRuntime :exec
INSERT INTO inference_runtime
  (tenant_id, service_id, generation, runtime_mode, runtime_phase, publication_phase, invocation_health)
VALUES ($1, $2, $3, $4, 'unknown', 'withdrawn', 'unknown');

-- name: PutIdempotency :execrows
INSERT INTO inference_idempotency_requests
  (tenant_id, method, idempotency_key, payload_hash, operation_id, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (tenant_id, method, idempotency_key) DO NOTHING;

-- name: GetIdempotency :one
SELECT tenant_id, method, idempotency_key, payload_hash, operation_id, created_at, expires_at
FROM inference_idempotency_requests
WHERE tenant_id = $1 AND method = $2 AND idempotency_key = $3;

-- name: GetOperationService :one
SELECT service_id
FROM inference_operations
WHERE tenant_id = $1 AND id = $2;

-- name: GetOperation :one
SELECT tenant_id, id, service_id, kind, phase, step, target_generation, request_hash,
       attempt, retry_at, lease_owner, lease_until, lease_token,
       error_code, error_message, result_snapshot, created_at, updated_at, completed_at
FROM inference_operations
WHERE tenant_id = $1 AND id = $2;

-- name: ListOperations :many
SELECT tenant_id, id, service_id, kind, phase, step, target_generation,
       request_hash, attempt, retry_at, lease_owner, lease_until, lease_token,
       error_code, error_message, result_snapshot, created_at, updated_at,
       completed_at
FROM inference_operations
WHERE tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.arg(service_id)::uuid IS NULL OR service_id = sqlc.arg(service_id)::uuid)
  AND (sqlc.arg(cursor_created_at)::timestamptz IS NULL
       OR (created_at, id) <
          (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: GetLeasedOperation :one
-- The resource_work lease is the execution fence for operation steps.  A
-- worker must reload the current operation while holding that lease.
SELECT o.tenant_id, o.id, o.service_id, o.kind, o.phase, o.step,
       o.target_generation, o.request_hash, o.attempt, o.retry_at,
       o.lease_owner, o.lease_until, o.lease_token, o.error_code,
       o.error_message, o.result_snapshot, o.created_at, o.updated_at,
       o.completed_at
FROM inference_operations o
JOIN inference_services s
  ON s.tenant_id = o.tenant_id AND s.id = o.service_id
JOIN inference_resource_work w
  ON w.tenant_id = o.tenant_id AND w.service_id = o.service_id
WHERE o.tenant_id = $1 AND o.id = s.current_operation_id
  AND o.target_generation = s.desired_generation
  AND w.lease_token = $2 AND w.lease_until > clock_timestamp();

-- name: GetOperationAuditContext :one
-- The acceptance event is the durable source of caller context. Worker step
-- events reuse it after the synchronous request has returned.
SELECT actor, request_id
FROM inference_audit_events
WHERE tenant_id = $1
  AND operation_id = $2
  AND event_type LIKE 'inference.%.accepted'
ORDER BY created_at ASC, event_id ASC
LIMIT 1;

-- name: GetQuotaReservation :one
SELECT tenant_id, service_id, operation_id, generation, reservation_id,
       requested_resources, state, last_error_code, created_at, updated_at
FROM inference_quota_reservations
WHERE tenant_id = $1 AND operation_id = $2;

-- name: GetActiveQuotaReservation :one
SELECT tenant_id, service_id, operation_id, generation, reservation_id,
       requested_resources, state, last_error_code, created_at, updated_at
FROM inference_quota_reservations
WHERE tenant_id = $1 AND service_id = $2 AND generation <= $3
  AND state IN ('pending', 'reserved', 'confirmed', 'release_pending')
ORDER BY generation DESC, updated_at DESC
LIMIT 1;

-- name: GetPreviousQuotaReservation :one
SELECT tenant_id, service_id, operation_id, generation, reservation_id,
       requested_resources, state, last_error_code, created_at, updated_at
FROM inference_quota_reservations
WHERE tenant_id = $1 AND service_id = $2 AND generation < $3
  AND state IN ('reserved', 'confirmed', 'release_pending')
ORDER BY generation DESC, updated_at DESC
LIMIT 1;

-- name: UpdateQuotaReservation :execrows
UPDATE inference_quota_reservations
SET reservation_id = $3, requested_resources = $4, state = $5,
    last_error_code = $6, updated_at = clock_timestamp()
WHERE tenant_id = $1 AND operation_id = $2 AND generation = $7;

-- name: UpdateQuotaReservationFenced :execrows
UPDATE inference_quota_reservations r
SET reservation_id = sqlc.arg(reservation_id), requested_resources = sqlc.arg(requested_resources), state = sqlc.arg(state),
    last_error_code = sqlc.arg(last_error_code), updated_at = clock_timestamp()
FROM inference_operations o
JOIN inference_resource_work w
  ON w.tenant_id = o.tenant_id AND w.service_id = o.service_id
JOIN inference_services s
  ON s.tenant_id = o.tenant_id AND s.id = o.service_id
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.operation_id = sqlc.arg(operation_id)
  AND r.generation = sqlc.arg(generation)
  AND o.tenant_id = r.tenant_id
  AND o.target_generation >= r.generation
  AND o.id = sqlc.arg(fence_operation_id)
  AND s.current_operation_id = o.id
  AND w.lease_token = sqlc.arg(lease_token) AND w.lease_until > clock_timestamp();

-- name: GetPublication :one
SELECT tenant_id, service_id, generation, desired_phase, observed_phase,
       invocation_url, owned_kind, owned_namespace, owned_name, owned_uid,
       owned_resource_version, last_error_code, observed_at, updated_at
FROM inference_publications
WHERE tenant_id = $1 AND service_id = $2 AND generation = $3;

-- name: GetLatestPublication :one
SELECT tenant_id, service_id, generation, desired_phase, observed_phase,
       invocation_url, owned_kind, owned_namespace, owned_name, owned_uid,
       owned_resource_version, last_error_code, observed_at, updated_at
FROM inference_publications
WHERE tenant_id = $1 AND service_id = $2 AND generation <= $3
ORDER BY generation DESC, updated_at DESC
LIMIT 1;

-- name: UpsertPublication :exec
INSERT INTO inference_publications
  (tenant_id, service_id, generation, desired_phase, observed_phase,
   invocation_url, last_error_code)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (tenant_id, service_id, generation) DO UPDATE
SET desired_phase = EXCLUDED.desired_phase,
    observed_phase = CASE
      WHEN EXCLUDED.desired_phase IN ('publishing', 'withdrawing')
        THEN inference_publications.observed_phase
      ELSE EXCLUDED.observed_phase
    END,
    invocation_url = EXCLUDED.invocation_url,
    last_error_code = EXCLUDED.last_error_code,
    updated_at = clock_timestamp();

-- name: UpsertPublicationFenced :execrows
INSERT INTO inference_publications
  (tenant_id, service_id, generation, desired_phase, observed_phase,
   invocation_url, last_error_code)
SELECT sqlc.arg(tenant_id), sqlc.arg(service_id), sqlc.arg(generation), sqlc.arg(desired_phase), sqlc.arg(observed_phase), sqlc.arg(invocation_url), sqlc.arg(last_error_code)
WHERE EXISTS (
  SELECT 1
  FROM inference_operations o
  JOIN inference_resource_work w
    ON w.tenant_id = o.tenant_id AND w.service_id = o.service_id
  JOIN inference_services s
    ON s.tenant_id = o.tenant_id AND s.id = o.service_id
  WHERE o.tenant_id = sqlc.arg(tenant_id) AND o.id = sqlc.arg(fence_operation_id)
    AND o.target_generation >= sqlc.arg(generation)
    AND s.current_operation_id = o.id
    AND w.lease_token = sqlc.arg(lease_token) AND w.lease_until > clock_timestamp()
)
ON CONFLICT (tenant_id, service_id, generation) DO UPDATE
SET desired_phase = EXCLUDED.desired_phase,
    observed_phase = CASE
      WHEN EXCLUDED.desired_phase IN ('publishing', 'withdrawing')
        THEN inference_publications.observed_phase
      ELSE EXCLUDED.observed_phase
    END,
    invocation_url = EXCLUDED.invocation_url,
    last_error_code = EXCLUDED.last_error_code,
    updated_at = clock_timestamp();

-- name: InsertQuotaReservation :exec
INSERT INTO inference_quota_reservations
  (tenant_id, service_id, operation_id, generation, reservation_id, requested_resources, state)
VALUES ($1, $2, $3, $4, $5, $6, 'pending');

-- name: AdvanceOperationStepCAS :execrows
-- A worker may advance an operation only while it is still the current
-- operation for the same desired generation.  The expected phase and step
-- make retries/stale workers harmless; retry_at prevents an early retry from
-- racing the worker that owns the current lease.
UPDATE inference_operations o
SET phase = sqlc.arg(next_phase),
    step = sqlc.arg(next_step),
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    retry_at = NULL,
    completed_at = CASE
        WHEN sqlc.arg(next_phase) IN ('succeeded', 'failed') THEN clock_timestamp()
        ELSE NULL
    END,
    updated_at = clock_timestamp()
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE o.tenant_id = sqlc.arg(tenant_id)
  AND o.id = sqlc.arg(operation_id)
  AND o.target_generation = sqlc.arg(target_generation)
  AND o.phase = sqlc.arg(expected_phase)
  AND o.step = sqlc.arg(expected_step)
  AND (o.retry_at IS NULL OR o.retry_at <= clock_timestamp())
  AND s.tenant_id = o.tenant_id
  AND s.id = o.service_id
  AND s.current_operation_id = o.id
  AND s.desired_generation = o.target_generation
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();

-- name: RetryOperationStepCAS :execrows
-- Retry keeps the step durable and schedules another attempt.  It is fenced
-- by the same aggregate/generation predicates as a normal step transition.
UPDATE inference_operations o
SET phase = 'pending',
    attempt = o.attempt + 1,
    retry_at = clock_timestamp() + sqlc.arg(retry_seconds)::double precision * interval '1 second',
    lease_owner = NULL,
    lease_until = NULL,
    lease_token = NULL,
    error_code = sqlc.arg(error_code),
    error_message = sqlc.arg(error_message),
    updated_at = clock_timestamp()
FROM inference_services s
JOIN inference_resource_work w
  ON w.tenant_id = s.tenant_id AND w.service_id = s.id
WHERE o.tenant_id = sqlc.arg(tenant_id)
  AND o.id = sqlc.arg(operation_id)
  AND o.target_generation = sqlc.arg(target_generation)
  AND o.phase = sqlc.arg(expected_phase)
  AND o.step = sqlc.arg(expected_step)
  AND s.tenant_id = o.tenant_id
  AND s.id = o.service_id
  AND s.current_operation_id = o.id
  AND s.desired_generation = o.target_generation
  AND w.lease_token = sqlc.arg(lease_token)
  AND w.lease_until > clock_timestamp();
