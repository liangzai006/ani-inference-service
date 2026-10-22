-- Inference-owned data-plane credentials. Governance platform keys are kept
-- in a separate service and are never copied into this relation.
BEGIN;

CREATE TABLE inference_access_keys (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    name TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    secret_hash TEXT NOT NULL,
    hash_version TEXT NOT NULL,
    allowed_service_ids TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    allowed_model_names TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    requests_per_second INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_second >= 0),
    requests_per_minute INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_minute >= 0),
    requests_per_hour INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_hour >= 0),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    create_idempotency_key TEXT,
    rotation_idempotency_key TEXT,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (key_prefix),
    UNIQUE (tenant_id, create_idempotency_key)
);

-- The prefix is the only lookup available before the tenant is known. It is
-- globally unique and non-secret; the Argon2id hash remains the authority.
CREATE INDEX inference_access_keys_tenant_created_idx
    ON inference_access_keys (tenant_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX inference_access_keys_rotation_idempotency_idx
    ON inference_access_keys (tenant_id, id, rotation_idempotency_key)
    WHERE rotation_idempotency_key IS NOT NULL;

CREATE TABLE inference_access_key_audit_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    key_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    request_id TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, event_id),
    FOREIGN KEY (tenant_id, key_id)
      REFERENCES inference_access_keys (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX inference_access_key_audit_key_idx
    ON inference_access_key_audit_events (tenant_id, key_id, created_at DESC);

CREATE TABLE inference_access_key_rate_buckets (
    tenant_id UUID NOT NULL,
    key_id UUID NOT NULL,
    window_name TEXT NOT NULL CHECK (window_name IN ('second', 'minute', 'hour')),
    window_start TIMESTAMPTZ NOT NULL,
    request_count BIGINT NOT NULL DEFAULT 0 CHECK (request_count >= 0),
    PRIMARY KEY (tenant_id, key_id, window_name, window_start),
    FOREIGN KEY (tenant_id, key_id)
      REFERENCES inference_access_keys (tenant_id, id) ON DELETE CASCADE
);

COMMIT;
