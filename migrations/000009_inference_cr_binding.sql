-- Persist the InferenceService projection identity in the same fenced binding
-- relation used for runtime objects. It is a control projection, not a second
-- business authority.
ALTER TABLE inference_runtime_bindings
    DROP CONSTRAINT inference_runtime_bindings_object_kind_check,
    ADD CONSTRAINT inference_runtime_bindings_object_kind_check
        CHECK (object_kind IN ('InferenceService', 'Deployment', 'Service', 'Pod', 'LeaderWorkerSet'));

ALTER TABLE inference_runtime_bindings
    DROP CONSTRAINT inference_runtime_bindings_role_check,
    ADD CONSTRAINT inference_runtime_bindings_role_check
        CHECK (role IN ('control', 'runtime', 'endpoint', 'member'));
