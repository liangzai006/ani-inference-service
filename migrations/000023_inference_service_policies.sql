-- Direct service binding, tenant-scoped policy facts and shared caller quotas.
BEGIN;
CREATE TABLE inference_service_policies (
 tenant_id UUID NOT NULL,
 service_id UUID NOT NULL,
 allowed_tenants TEXT[] NOT NULL,
 rate_limits JSONB NOT NULL DEFAULT '{}'::jsonb,
 version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id, service_id),
 FOREIGN KEY (tenant_id, service_id) REFERENCES inference_services(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE inference_service_policy_audit_events (
 tenant_id UUID NOT NULL,
 service_id UUID NOT NULL,
 event_id UUID NOT NULL,
 actor TEXT NOT NULL,
 request_id TEXT NOT NULL,
 payload JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY (tenant_id,event_id),
 FOREIGN KEY (tenant_id,service_id) REFERENCES inference_services(tenant_id,id) ON DELETE CASCADE
);
CREATE TABLE inference_service_rate_buckets (
 tenant_id UUID NOT NULL,
 service_id UUID NOT NULL,
 caller_tenant_id UUID NOT NULL,
 window_name TEXT NOT NULL CHECK (window_name IN ('minute','hour')),
 window_start TIMESTAMPTZ NOT NULL,
 request_count BIGINT NOT NULL CHECK (request_count >= 0),
 PRIMARY KEY (tenant_id,service_id,caller_tenant_id,window_name,window_start),
 FOREIGN KEY (tenant_id,service_id) REFERENCES inference_services(tenant_id,id) ON DELETE CASCADE
);
COMMIT;
