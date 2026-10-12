-- Independent Inference control plane. No ANI shared tables and no RLS.
-- Every tenant-owned relation carries tenant_id; all relations use composite keys.

CREATE TABLE inference_services (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    name TEXT NOT NULL,
    desired_state TEXT NOT NULL CHECK (desired_state IN ('running', 'stopped', 'deleted')),
    desired_generation BIGINT NOT NULL CHECK (desired_generation > 0),
    current_operation_id UUID,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);

CREATE TABLE inference_specs (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    service_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    -- Optional external Model-domain identity; Inference owns only the
    -- version/artifact reference and never the Model record itself.
    model_id UUID,
    model_version_id UUID NOT NULL,
    artifact_provider TEXT NOT NULL,
    artifact_ref TEXT NOT NULL,
    artifact_sha256 TEXT NOT NULL,
    image_ref TEXT NOT NULL,
    served_model_name TEXT NOT NULL,
    engine_runtime TEXT NOT NULL,
    command_argv JSONB NOT NULL DEFAULT '[]'::jsonb,
    resources JSONB NOT NULL DEFAULT '{}'::jsonb,
    replicas INTEGER NOT NULL DEFAULT 1 CHECK (replicas > 0),
    spec_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, service_id, generation),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE TABLE inference_operations (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    service_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('create', 'update', 'start', 'stop', 'restart', 'delete')),
    phase TEXT NOT NULL CHECK (phase IN ('pending', 'running', 'succeeded', 'failed')),
    step TEXT NOT NULL CHECK (step IN ('admission', 'reserve_quota', 'apply_cr', 'apply_runtime', 'observe_runtime', 'withdraw_publication', 'publish', 'release_quota', 'complete')),
    target_generation BIGINT NOT NULL CHECK (target_generation > 0),
    request_hash TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    retry_at TIMESTAMPTZ,
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_token UUID,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    result_snapshot JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

ALTER TABLE inference_services ADD CONSTRAINT inference_services_current_operation_fk
    FOREIGN KEY (tenant_id, current_operation_id)
    REFERENCES inference_operations (tenant_id, id)
    DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE inference_idempotency_requests (
    tenant_id UUID NOT NULL,
    method TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    operation_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, method, idempotency_key),
    FOREIGN KEY (tenant_id, operation_id) REFERENCES inference_operations (tenant_id, id)
);

CREATE TABLE inference_runtime (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    cr_uid TEXT NOT NULL DEFAULT '',
    deployment_uid TEXT NOT NULL DEFAULT '',
    service_uid TEXT NOT NULL DEFAULT '',
    deployment_resource_version TEXT,
    runtime_phase TEXT NOT NULL CHECK (runtime_phase IN ('unknown', 'pending', 'loading', 'ready', 'degraded', 'stopped')),
    ready_replicas INTEGER NOT NULL DEFAULT 0 CHECK (ready_replicas >= 0),
    model_ready BOOLEAN NOT NULL DEFAULT FALSE,
    publication_phase TEXT NOT NULL CHECK (publication_phase IN ('withdrawn', 'publishing', 'published', 'withdrawing', 'unknown')),
    invocation_health TEXT NOT NULL CHECK (invocation_health IN ('unknown', 'healthy', 'unhealthy')),
    runtime_endpoint TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ,
    stale_after TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE TABLE inference_resource_work (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    dirty_version BIGINT NOT NULL DEFAULT 0 CHECK (dirty_version >= 0),
    acknowledged_version BIGINT NOT NULL DEFAULT 0 CHECK (acknowledged_version <= dirty_version),
    next_run_at TIMESTAMPTZ,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    lease_owner TEXT,
    lease_until TIMESTAMPTZ,
    lease_token UUID,
    last_error_code TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, service_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id)
);

CREATE TABLE inference_quota_reservations (
    tenant_id UUID NOT NULL,
    service_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    reservation_id TEXT NOT NULL,
    requested_resources JSONB NOT NULL DEFAULT '{}'::jsonb,
    state TEXT NOT NULL CHECK (state IN ('pending', 'reserved', 'confirmed', 'release_pending', 'released', 'failed')),
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, operation_id),
    UNIQUE (tenant_id, reservation_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id),
    FOREIGN KEY (tenant_id, operation_id) REFERENCES inference_operations (tenant_id, id)
);

CREATE TABLE inference_audit_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    service_id UUID NOT NULL,
    operation_id UUID,
    generation BIGINT NOT NULL CHECK (generation > 0),
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    before_state JSONB,
    after_state JSONB,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, event_id),
    FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services (tenant_id, id),
    FOREIGN KEY (tenant_id, operation_id) REFERENCES inference_operations (tenant_id, id)
);

CREATE INDEX inference_services_list_idx ON inference_services (tenant_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX inference_operations_claim_idx ON inference_operations (tenant_id, phase, retry_at, created_at);
CREATE INDEX inference_resource_work_due_idx ON inference_resource_work (tenant_id, next_run_at, updated_at) WHERE next_run_at IS NOT NULL;
CREATE INDEX inference_audit_service_idx ON inference_audit_events (tenant_id, service_id, created_at DESC);
