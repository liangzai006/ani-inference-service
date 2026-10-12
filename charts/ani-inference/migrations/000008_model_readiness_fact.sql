-- Distinguish a confirmed model readiness result from an unknown result.
-- The model provider owns this fact; runtime observation must not infer it.
ALTER TABLE inference_runtime
    ADD COLUMN model_ready_known BOOLEAN NOT NULL DEFAULT FALSE;
