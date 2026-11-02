-- A placement identifies the platform-owned Namespace itself, not just its
-- reusable name. Existing services have no row until explicitly handled.
CREATE TABLE inference_namespace_placements (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    namespace_name TEXT NOT NULL CHECK (namespace_name <> ''),
    namespace_uid TEXT NOT NULL CHECK (namespace_uid <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);
