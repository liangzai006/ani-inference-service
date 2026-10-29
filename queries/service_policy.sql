-- name: LockPolicyServiceShared :one
SELECT id FROM inference_services WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL AND desired_state<>'deleted' FOR SHARE;


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
