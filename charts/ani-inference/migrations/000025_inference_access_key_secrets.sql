-- Recoverable credentials are encrypted with an externally managed key.
-- NULL identifies legacy hashes; verification can archive their original AK.
BEGIN;
ALTER TABLE inference_access_keys ADD COLUMN secret_ciphertext TEXT;
COMMIT;
