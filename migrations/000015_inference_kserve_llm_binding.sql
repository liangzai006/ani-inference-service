-- KServe LLMInferenceService objects are durable runtime bindings just like
-- the single-node InferenceService object. KServe owns the generated
-- LeaderWorkerSet and workload Service.
ALTER TABLE inference_runtime_bindings
    DROP CONSTRAINT inference_runtime_bindings_object_kind_check,
    ADD CONSTRAINT inference_runtime_bindings_object_kind_check
        CHECK (object_kind IN ('InferenceService', 'KServeInferenceService', 'KServeLLMInferenceService', 'Deployment', 'Service', 'Pod', 'LeaderWorkerSet'));
