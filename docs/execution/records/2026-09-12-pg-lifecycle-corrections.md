# PostgreSQL lifecycle integration corrections

## Scope and observed failures

This run uses the real PostgreSQL repository, sqlc queries, operation store,
resource-work leases, runner and read use case. Kubernetes, model, admission,
quota authority and publication providers are fixtures. A new worker and store
are constructed for each step; this exercises durable resumption, not a real
operating-system process restart.

The create lifecycle first failed at quota Reserve: the stored nested
`requests/limits` JSON was decoded into `map[string]string`, and the ignored
error produced `{"limits":"","requests":""}`. After repairing this boundary,
the test reached completion but GET still reported `publication_phase=withdrawn`
although the publication row was `published`.

## Changes

- Quota reservation resources reuse `resources.Spec` with lowercase JSON field
  names. Current and previous reservations retain CPU, memory and GPU requests
  and limits. Malformed shapes, null snapshots and invalid quantities fail
  explicitly; no external quota ledger or gRPC field was added.
- GET and LIST read the publication owner's latest row bounded by the service's
  desired generation. An old runtime projection no longer hides confirmed
  publication. Queries remain tenant scoped and read only.
- Every durable notification increments `dirty_version` independently from
  business generation and makes work due. An event received during execution
  survives that worker's acknowledgement. Without another event, successful work
  returns to the configured observation interval.
- Recovery's repair loop is shared through a tenant-scoped helper. The recovery
  test calls that helper only for its generated tenant, avoiding a global wake-up
  across unrelated records in the development database.
- PostgreSQL test fixtures now clear their foreign-key references before deleting
  their generated tenant records, report cleanup errors, and keep the connection
  pool open until cleanup finishes. Existing unrelated records were not removed.

## Verification

- `TestPostgresCreateLifecyclePreservesQuotaResources`: create through confirmed
  publication, reconstructed workers, immutable resource snapshot round-trip,
  current/previous reservation loading, corrupt-snapshot rejection, nine atomic
  acceptance/claim/transition audit events and consistent GET/LIST publication.
- `TestWorkStoreSameGenerationNotificationSurvivesAcknowledgement`: idle and
  in-flight same-generation events, old-generation hints, preserved dirty work,
  and return to normal observation scheduling.
- `TestReservationSnapshotRejectsInvalidResources`: null, unexpected fields,
  wrong JSON types, negative/invalid quantities and requests exceeding limits.

Targeted real PostgreSQL checks passed. Final developer-machine verification with
`INFERENCE_PG_DSN` configured also passed: `go test -p 1 ./... -count=1`
(PostgreSQL package: 2.086s; command/process package: 38.508s),
`go vet -p 1 ./...`, `go build -p 1 ./...` and `buf lint`.
The initial failing outputs above were observed before the corresponding fixes.
Migration DDL is unchanged; sqlc output was regenerated from the existing query
source. The command package verifies process startup and shutdown; it does not
prove recovery of an interrupted business operation across process restarts.

## Limits and next work

This is PostgreSQL integration evidence, not a deployed inference service or
real external quota accounting. Quota totals for replicas/LWS, versioned authority
adapters, cross-generation publication on update/restart, stop/delete recovery,
IAM, Service endpoints, model loading and real Kubernetes/engine/data-plane
validation remain incomplete. Local resource snapshots are per-container inputs;
they do not yet prove aggregate quota enforcement.
