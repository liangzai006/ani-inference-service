-- KServe InferenceService objects are runtime bindings owned by ANI only at
-- the custom-resource boundary. KServe owns the generated Deployment/Service.
ALTER TABLE inference_runtime_bindings
    DROP CONSTRAINT inference_runtime_bindings_object_kind_check,
    ADD CONSTRAINT inference_runtime_bindings_object_kind_check
        CHECK (object_kind IN ('InferenceService', 'KServeInferenceService', 'Deployment', 'Service', 'Pod', 'LeaderWorkerSet'));
