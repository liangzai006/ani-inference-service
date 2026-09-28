-- GPU scheduling is an immutable accelerator request/plan snapshot.  The
-- accelerator service owns resolution; these fields only preserve the exact
-- request and returned plan alongside each inference generation.
ALTER TABLE inference_specs
    ADD COLUMN gpu_request JSONB,
    ADD COLUMN gpu_plan JSONB,
    ADD COLUMN gpu_plan_digest TEXT;
