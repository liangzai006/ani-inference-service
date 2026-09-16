-- Extend the durable operation vocabulary for fenced runtime removal and
-- explicit absence confirmation. No shared ANI/Core table is changed.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;

ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'apply_cr', 'apply_runtime',
        'observe_runtime', 'withdraw_publication', 'delete_runtime',
        'observe_absence', 'publish', 'release_quota', 'complete'
    ));
