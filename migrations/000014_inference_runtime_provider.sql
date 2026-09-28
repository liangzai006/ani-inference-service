-- Runtime provider is selected per immutable service generation. Existing
-- rows retain the deployment provider and older clients remain compatible.
ALTER TABLE inference_specs
  ADD COLUMN runtime_provider TEXT NOT NULL DEFAULT 'deployment';

ALTER TABLE inference_specs
  ADD CONSTRAINT inference_specs_runtime_provider_check
  CHECK (runtime_provider IN ('deployment', 'kserve'));
