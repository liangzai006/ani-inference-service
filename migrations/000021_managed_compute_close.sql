-- Explicit expansion of the existing managed intake; no service/operation is
-- fabricated for a DELETE that overtakes the first accepted CREATE.
BEGIN;
ALTER TABLE inference_managed_gpu_resources
 ADD COLUMN resource_class TEXT NOT NULL DEFAULT 'gpu' CHECK (resource_class IN ('cpu','gpu')),
 ADD COLUMN create_command_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE inference_managed_gpu_resources DROP CONSTRAINT inference_managed_gpu_resources_metering_version_check;
ALTER TABLE inference_managed_gpu_resources ALTER COLUMN metering_version DROP NOT NULL;
ALTER TABLE inference_managed_gpu_resources ADD CHECK
 ((resource_class='cpu' AND metering_version IS NULL) OR
  (resource_class='gpu' AND metering_version='gpu-metering-v1'));
ALTER TABLE inference_managed_gpu_commands
 ADD COLUMN actor TEXT NOT NULL DEFAULT '',
 ADD COLUMN close_state TEXT NOT NULL DEFAULT '' CHECK (close_state IN ('','pending','unknown_execution','complete')),
 ADD COLUMN lease_token UUID,
 ADD COLUMN lease_until TIMESTAMPTZ,
 ADD COLUMN retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN close_error TEXT NOT NULL DEFAULT '',
 ADD COLUMN release_evidence_id TEXT NOT NULL DEFAULT '',
 ADD COLUMN release_evidence_digest TEXT NOT NULL DEFAULT '',
 ADD COLUMN release_report JSONB,
 ADD COLUMN completed_at TIMESTAMPTZ;
CREATE INDEX inference_managed_close_pending ON inference_managed_gpu_commands(tenant_id,retry_at)
 WHERE kind='delete' AND close_state IN ('pending','unknown_execution');
-- Read-only projection includes only commands actually accepted by this owner.
-- Original operation rows win, avoiding duplicate items and totals.
CREATE VIEW inference_operation_projection AS
SELECT o.tenant_id,o.id,o.service_id,o.kind,o.phase,
 CASE WHEN o.kind='delete' AND o.phase NOT IN ('succeeded','failed') AND c.release_report IS NOT NULL
      THEN 'report_resource_released' ELSE o.step END::text AS step,
 o.target_generation,o.request_hash,o.attempt,o.retry_at,o.lease_owner,o.lease_until,
 o.lease_token,o.error_code,o.error_message,o.result_snapshot,o.created_at,o.updated_at,o.completed_at
FROM inference_operations o LEFT JOIN inference_managed_gpu_commands c
 ON c.tenant_id=o.tenant_id AND c.operation_id=o.id
UNION ALL
SELECT c.tenant_id,c.operation_id,c.resource_id,c.kind,
 CASE WHEN c.execution_intent='blocked_by_delete' THEN 'failed'
      WHEN c.close_state='complete' THEN 'succeeded' ELSE 'pending' END,
 CASE WHEN c.execution_intent='blocked_by_delete' THEN 'cancelled_by_delete'
      WHEN c.close_state='complete' THEN 'complete'
      WHEN c.release_report IS NOT NULL THEN 'report_resource_released'
      WHEN c.close_state='unknown_execution' THEN 'recover_execution'
      WHEN c.close_state='pending' THEN 'observe_release' ELSE 'closing_intent' END,
 0::bigint,c.request_hash,c.attempt,c.retry_at,NULL::text,c.lease_until,c.lease_token,
 CASE WHEN c.execution_intent='blocked_by_delete' THEN 'cancelled_by_delete' ELSE '' END,
 c.close_error,convert_from(c.response_snapshot,'UTF8')::jsonb,c.created_at,c.created_at,
 CASE WHEN c.execution_intent='blocked_by_delete' THEN c.created_at ELSE c.completed_at END
FROM inference_managed_gpu_commands c
WHERE (c.kind='delete' OR c.execution_intent='blocked_by_delete')
 AND NOT EXISTS(SELECT 1 FROM inference_operations o WHERE o.tenant_id=c.tenant_id AND o.id=c.operation_id);

COMMIT;
