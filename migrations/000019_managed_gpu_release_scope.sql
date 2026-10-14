-- Persist the exact Pod UID scope used to prove a managed GPU release.
--
-- The scope is intentionally independent from inference_services and
-- inference_operations foreign keys. A Governance-owned DELETE can arrive
-- before the local service/operation row exists; retaining the tenant,
-- service and operation identity still lets the later worker retry the same
-- ObserveRelease request without reconstructing names or UIDs.
CREATE TABLE inference_managed_gpu_release_scopes (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    pods JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id, operation_id),
    CHECK (jsonb_typeof(pods) = 'array'),
    CHECK (jsonb_array_length(pods) > 0)
);

CREATE INDEX inference_managed_gpu_release_scopes_lookup_idx
    ON inference_managed_gpu_release_scopes (tenant_id, service_id, operation_id);

