-- name: InsertNamespacePlacement :exec
INSERT INTO inference_namespace_placements (tenant_id, service_id, namespace_name, namespace_uid)
VALUES ($1, $2, $3, $4);

-- name: GetNamespacePlacement :one
SELECT namespace_name, namespace_uid FROM inference_namespace_placements
WHERE tenant_id = $1 AND service_id = $2;
