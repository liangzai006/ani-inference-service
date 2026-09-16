-- Update replaces a running generation. Release its previous confirmed
-- reservation only after publication withdrawal and runtime absence, before
-- reserving the new generation.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_kind_check;
ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_kind_check
    CHECK (kind IN ('create', 'update', 'start', 'stop', 'restart', 'delete'));

ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;
ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'release_previous_quota', 'apply_cr',
        'apply_runtime', 'observe_runtime', 'withdraw_publication',
        'delete_runtime', 'observe_absence', 'delete_cr', 'publish',
        'release_quota', 'complete'
    ));
