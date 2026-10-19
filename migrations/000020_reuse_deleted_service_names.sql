-- Deleted services remain as tombstones for audit and operation history.
-- Their names must be reusable by a new active service in the same tenant.
ALTER TABLE inference_services
    DROP CONSTRAINT inference_services_tenant_id_name_key;

CREATE UNIQUE INDEX inference_services_active_tenant_name_key
    ON inference_services (tenant_id, name)
    WHERE deleted_at IS NULL;
