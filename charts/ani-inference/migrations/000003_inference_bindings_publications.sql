-- Separate binding/publication facts from the current runtime projection.
-- These tables remain owned by Inference and every relation is tenant scoped.

CREATE TABLE inference_runtime_bindings (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    object_kind TEXT NOT NULL CHECK (object_kind IN ('Deployment', 'Service', 'Pod', 'LeaderWorkerSet')),
    object_namespace TEXT NOT NULL,
    object_name TEXT NOT NULL,
    object_uid TEXT NOT NULL,
    resource_version TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL CHECK (role IN ('runtime', 'endpoint', 'member')),
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id, generation, object_kind, role),
    UNIQUE (tenant_id, service_id, generation, object_kind, object_namespace, object_name),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE TABLE inference_publications (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    desired_phase TEXT NOT NULL CHECK (desired_phase IN ('withdrawn', 'publishing', 'published', 'withdrawing')),
    observed_phase TEXT NOT NULL CHECK (observed_phase IN ('withdrawn', 'publishing', 'published', 'withdrawing', 'unknown')),
    invocation_url TEXT NOT NULL DEFAULT '',
    owned_kind TEXT NOT NULL DEFAULT '',
    owned_namespace TEXT NOT NULL DEFAULT '',
    owned_name TEXT NOT NULL DEFAULT '',
    owned_uid TEXT NOT NULL DEFAULT '',
    owned_resource_version TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id, generation),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE TABLE inference_observation_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    service_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    source_kind TEXT NOT NULL,
    source_uid TEXT NOT NULL DEFAULT '',
    source_resource_version TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (tenant_id, event_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE INDEX inference_runtime_bindings_lookup_idx
    ON inference_runtime_bindings (tenant_id, service_id, generation, object_uid);
CREATE INDEX inference_publications_current_idx
    ON inference_publications (tenant_id, service_id, generation DESC);
CREATE INDEX inference_observation_events_service_idx
    ON inference_observation_events (tenant_id, service_id, observed_at DESC);
