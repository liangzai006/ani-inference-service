# Task 3 — Inference ACC runtime migration

## Implemented contract

Inference now consumes ACC `accelerator/v1.ResolvedGpuRequest` through a
transport-neutral `gpu.Snapshot` containing `Execution`, `Display`, and ACC
quota items. The domain package has no protobuf, Kubernetes, or storage
imports. ACC execution maps are copied directly into the snapshot and the
KServe Deployment renderer projects those values without rebuilding profiles,
memory encodings, resource totals, or charge vectors. The GPU LWS adapter now
returns an explicit unsupported error; it never silently downgrades to an LWS.

Managed CREATE/UPDATE/DELETE paths carry the Inference-owned cluster and
command identity around the frozen execution. DELETE restores the immutable
cluster/snapshot from the persisted CREATE context instead of accepting a
caller-supplied replacement. Existing PostgreSQL JSON columns remain in place
for a minimal schema migration; new rows contain the Execution/Display
snapshot and execution digest. Old provider charge fields are never generated.

Release observation sends cluster ID, sealed timestamp, and the complete
historical Pod UID scope to ACC. Inference only reports the four stable facts
(`release_event_id`, `create_operation_id`, `resource_id`, `reason`) through
Governance `ReportResourceReleased`; Governance remains the source of the
original quota ledger. Idempotency, DELETE-first, unknown observations, and
stop/restart no-refund behavior remain guarded by the existing lifecycle.

The Governance quota API dependency was advanced to the revision that exports
`ReportResourceReleased`; no Governance source was changed.

## Test coverage retained or rewritten

- `internal/biz/gpu`: request/topology validation, pod-count arithmetic,
  execution snapshot validation, opaque resource projection, stable digest
  ordering, and four-field release identity/reason/owner checks.
- `internal/data/accelerator`: nil-request no-op, resolver/cluster fencing,
  tenant/actor metadata, exact ACC Execution/Display mapping, and release
  observation cluster/sealed/pod scope.
- `internal/data/governance`: four-field request shape, stable retry,
  invalid identity rejection, RPC/timeout propagation, and TLS CA/SAN checks.
- `internal/data/kubernetes`: ordinary Deployment projection keeps scheduler,
  queue, selectors, annotations, resources, and complete command argv; GPU LWS
  tests now assert the explicit unsupported boundary and caller-object
  immutability.
- `internal/data/postgres`: managed command atomic replay, concurrent ACK
  single-write behavior, immutable shared/whole Execution projection,
  DELETE-first closure and late CREATE blocking, forged identity/digest and
  rollback rejection, snapshot JSON/quantity round trips, and no persisted
  original-charge fields. PostgreSQL lifecycle tests still skip only when
  `INFERENCE_PG_DSN` is absent.

## Verification

Passed:

- `go test ./...`
- `go vet ./...`
- `./scripts/generate-api.sh generate`
- `./scripts/generate-api.sh lint`
- `git diff --check`

There is no Makefile/lint target in this service; the pinned generation script
is the repository lint entry point.
