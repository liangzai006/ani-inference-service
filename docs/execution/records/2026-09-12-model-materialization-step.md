# 2026-09-12 Durable model materialization step

## Change

Model readiness is now an explicit durable operation step rather than an
implicit boolean hidden in runtime observation:

- create/start/update/restart paths advance from `apply_cr` to
  `materialize_model`, then to `apply_runtime` only after the Model adapter
  reports a known ready result;
- `ModelPort` is an explicit external boundary and remains unconfigured in the
  current composition;
- `UpdateModelObservationForWork` persists only `model_ready` and its reason,
  fenced by tenant, generation, resource-work lease and PostgreSQL time;
- materialization start clears any previous `model_ready` fact through
  `MarkModelMaterializingForWork`, so a prior generation cannot authorize a new
  publication while the replacement artifact is still loading;
- migration `000007_model_materialization_step.sql` extends the operation CHECK
  constraint without copying Model or Storage tables.

## Evidence

The runner unit test verifies the step ordering and the PostgreSQL package
continues to pass against the local `recycling-postgres` database. The real
artifact/model provider and engine load result remain `not_verified`.
