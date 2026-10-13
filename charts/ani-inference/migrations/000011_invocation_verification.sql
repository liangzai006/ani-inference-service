-- Invocation health is verified only after publication is confirmed.
-- This durable step prevents operation success from depending on an endpoint
-- that cannot exist before publication, while preserving retry after crashes.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;
ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'release_previous_quota', 'apply_cr',
        'materialize_model', 'apply_runtime', 'observe_runtime',
        'withdraw_publication', 'delete_runtime', 'observe_absence',
        'delete_cr', 'publish', 'verify_invocation', 'release_quota', 'complete'
    ));
