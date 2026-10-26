-- Make newly accepted services present explicit lifecycle states instead of
-- exposing an uninitialized projection as unknown. Keep unknown in the
-- constraints for genuinely inconclusive observations (for example a probe
-- that races a generation change).
UPDATE inference_runtime
SET runtime_phase = 'pending'
WHERE runtime_phase = 'unknown';

UPDATE inference_runtime
SET invocation_health = 'unhealthy',
    reason = CASE WHEN reason = '' THEN 'runtime_not_ready' ELSE reason END
WHERE invocation_health = 'unknown'
  AND (runtime_phase <> 'ready'
       OR publication_phase <> 'published'
       OR NOT model_ready_known
       OR NOT model_ready);
