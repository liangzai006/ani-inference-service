-- Model artifact materialization is a durable operation step. Inference owns
-- the orchestration gate; Model/Storage remain the artifact and volume
-- authorities behind the adapter.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;
ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'release_previous_quota', 'apply_cr',
        'materialize_model', 'apply_runtime', 'observe_runtime',
        'withdraw_publication', 'delete_runtime', 'observe_absence',
        'delete_cr', 'publish', 'release_quota', 'complete'
    ));
