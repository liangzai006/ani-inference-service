-- GPU resolution is an explicit durable gate before Kubernetes projection.
-- Generations without resource.gpu still pass through this step without
-- contacting an accelerator provider; the request was simply absent.
ALTER TABLE inference_operations
    DROP CONSTRAINT IF EXISTS inference_operations_step_check;

ALTER TABLE inference_operations
    ADD CONSTRAINT inference_operations_step_check
    CHECK (step IN (
        'admission', 'reserve_quota', 'resolve_gpu', 'release_previous_quota',
        'apply_cr', 'materialize_model', 'apply_runtime', 'observe_runtime',
        'withdraw_publication', 'delete_runtime', 'observe_absence',
        'delete_cr', 'publish', 'verify_invocation', 'release_quota',
        'complete'
    ));
