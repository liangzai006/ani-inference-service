-- Optional endpoint contract for the Inference-owned runtime Service.
-- NULL in all columns means no Service is requested; partial values are invalid.
ALTER TABLE inference_specs
  ADD COLUMN endpoint_container_port INTEGER,
  ADD COLUMN endpoint_service_port INTEGER,
  ADD COLUMN endpoint_target_port TEXT,
  ADD COLUMN endpoint_protocol TEXT;

ALTER TABLE inference_specs
  ADD CONSTRAINT inference_specs_endpoint_shape_check CHECK (
    (endpoint_container_port IS NULL AND endpoint_service_port IS NULL
      AND endpoint_target_port IS NULL AND endpoint_protocol IS NULL)
    OR (endpoint_container_port BETWEEN 1 AND 65535
      AND endpoint_service_port BETWEEN 1 AND 65535
      AND endpoint_target_port IS NOT NULL AND endpoint_target_port <> ''
      AND endpoint_protocol IN ('TCP', 'UDP', 'SCTP'))
  );
