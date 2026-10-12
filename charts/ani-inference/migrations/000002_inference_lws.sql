-- Add the distributed runtime shape without changing existing generations.
ALTER TABLE inference_specs
    ADD COLUMN runtime_mode TEXT NOT NULL DEFAULT 'deployment'
        CHECK (runtime_mode IN ('deployment', 'leader_worker_set')),
    ADD COLUMN worker_replicas INTEGER NOT NULL DEFAULT 1
        CHECK (worker_replicas > 0);

ALTER TABLE inference_runtime
    ADD COLUMN runtime_mode TEXT NOT NULL DEFAULT 'deployment'
        CHECK (runtime_mode IN ('deployment', 'leader_worker_set')),
    ADD COLUMN ready_groups INTEGER NOT NULL DEFAULT 0
        CHECK (ready_groups >= 0),
    ADD COLUMN ready_workers INTEGER NOT NULL DEFAULT 0
        CHECK (ready_workers >= 0),
    ADD COLUMN lws_uid TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX inference_operations_one_active
    ON inference_operations (tenant_id, service_id)
    WHERE phase IN ('pending', 'running');
