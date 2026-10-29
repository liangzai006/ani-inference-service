BEGIN;

CREATE TABLE inference_access_policies (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    name TEXT NOT NULL CHECK (name <> ''),
    allowed_model_names TEXT[] NOT NULL DEFAULT ARRAY[]::text[],
    requests_per_second INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_second >= 0),
    requests_per_minute INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_minute >= 0),
    requests_per_hour INTEGER NOT NULL DEFAULT 0 CHECK (requests_per_hour >= 0),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE inference_access_policy_audit_events (
    tenant_id UUID NOT NULL,
    event_id UUID NOT NULL,
    policy_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    actor TEXT NOT NULL,
    request_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, event_id)
);

-- Existing keys have no policy and are denied until explicitly rebound.
ALTER TABLE inference_access_keys ADD COLUMN policy_id UUID;
ALTER TABLE inference_access_keys ADD CONSTRAINT inference_access_keys_policy_fk
    FOREIGN KEY (tenant_id, policy_id) REFERENCES inference_access_policies (tenant_id, id);
CREATE INDEX inference_access_keys_policy_idx ON inference_access_keys (tenant_id, policy_id);
COMMIT;
