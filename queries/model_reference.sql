-- name: HasActiveModelVersionReferences :one
SELECT EXISTS (
 SELECT 1 FROM inference_specs sp
 JOIN inference_services s ON s.tenant_id=sp.tenant_id AND s.id=sp.service_id
 WHERE sp.tenant_id=$1 AND sp.model_version_id=ANY($2::uuid[])
   AND s.deleted_at IS NULL
) AS has_active_references;
