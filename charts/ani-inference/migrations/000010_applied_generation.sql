-- Keep the service-level applied projection explicit. It starts at zero and
-- advances only after a generation-fenced runtime observation is committed.
ALTER TABLE inference_services
    ADD COLUMN applied_generation BIGINT NOT NULL DEFAULT 0
        CHECK (applied_generation >= 0 AND applied_generation <= desired_generation);
