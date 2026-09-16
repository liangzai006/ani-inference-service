-- Delete removes the InferenceService projection only after runtime absence
-- has been confirmed. Runtime objects remain independently UID/RV fenced.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;

ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'apply_cr', 'apply_runtime',
        'observe_runtime', 'withdraw_publication', 'delete_runtime',
        'observe_absence', 'delete_cr', 'publish', 'release_quota', 'complete'
    ));
