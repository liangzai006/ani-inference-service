# Cross-generation lifecycle and deletion recovery

## Observed failures and corrections

The real PostgreSQL lifecycle test initially failed in three successive places:

1. Update applied runtime generation 2 but published generation 1. The operation
   store always loaded the latest publication at or below the desired generation.
   `StepPublish` now loads the exact target generation; withdrawal continues to
   load the previous publication. This preserves withdrawn history and avoids
   carrying the old route URL into a new generation.
2. Delete after a completed stop called quota Release with an empty reservation.
   The store intentionally returns only outstanding reservations. The terminal
   release step now completes without a provider call when none remain, including
   resumption after a release result has already been persisted. Database read
   errors still abort execution.
3. Delete committed its successful operation and service tombstone, but the work
   acknowledgement rejected deleted services and returned `ErrLeaseLost`.
   Acknowledgement now accepts the original valid lease and desired generation
   after tombstoning, clears the lease and leaves `next_run_at` null. Claim and
   retry still exclude tombstoned services.

## Verification: pass at PostgreSQL integration level

`TestPostgresReplacementAndDeletionLifecycle` runs
create → update → restart → stop → start → stop → delete against the real local
development PostgreSQL. Every step rebuilds its worker and operation store.
The test runs normally and with an injected interruption after either a quota
release result or a replacement publication result is persisted, before the
operation's terminal transition. Both retries complete from the stored step.

Assertions cover target-generation publication, retained withdrawn history,
withdrawal intent before the provider call, confirmed withdrawal before runtime
deletion, runtime absence before quota release, no repeated release after its
result is persisted, and terminal operation/tombstone/work acknowledgement.
Fixtures and cleanup are scoped to randomly generated test tenants.

Developer-machine checks passed after the fixes:

- `go test -p 1 ./... -count=1`, with `INFERENCE_PG_DSN` configured:
  PostgreSQL package 3.505s; command/process package 4.405s.
- `go vet -p 1 ./...` and `go build -p 1 ./...`.
- `sqlc generate` refreshed the work query output. No migration or Proto changed.
- Independent source review of these three corrections found no new regression.

## Evidence limits

The external admission, quota, model, Kubernetes runtime and publication ports
are fixtures. These tests do not verify real quota accounting, Kubernetes object
deletion, route withdrawal, GPU/LWS scheduling or an inference engine. The
interruptions are injected store errors, not operating-system process crashes.
Provider success before its result reaches PostgreSQL, stale remote writes after
lease loss, and restart after a tombstone commits but before work acknowledgement
still require separate failure-injection/real-dependency evidence.

The local quota projection now also derives replica/LWS demand from the immutable
spec using Kubernetes `Quantity` arithmetic. It still does not contact or replace
the Core quota authority. Remaining implementation includes that versioned
authority adapter and process wiring, Service endpoints, model materialization,
invocation health and rate-limit/data-plane integration. The overall refactor
remains active.
