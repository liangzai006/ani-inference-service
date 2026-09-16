# 2026-09-12 publication and generation fencing

## Publication intent

`OperationStore.SavePublication` previously wrote the same state to both
`desired_phase` and `observed_phase`. That made an unconfirmed
`withdrawing` or `publishing` request appear effective, which could hide a
still-published route or expose a route before provider confirmation.

The SQL upsert now preserves the existing `observed_phase` for intermediate
intent states and only changes it when the provider-confirmed state
(`withdrawn` or `published`) is saved. A first intent row uses `withdrawn` as
the safe observed baseline.

## Observation binding generation

`ReconcileStore.SaveObservationForWork` now requires every runtime object's
`BindingGeneration` to equal the claimed work item's generation. A positive
generation alone was insufficient because a delayed old-generation event
could otherwise enter the current observation transaction.

## Evidence

- PostgreSQL integration test `TestOperationStoreLoadsLeaseFencedOperation`
  verifies a published route remains observed as `published` while the
  withdrawal intent is `withdrawing`.
- PostgreSQL integration test `TestReconcileStoreObservationRequiresCurrentLease`
  rejects an old-generation binding before committing observation or binding
  changes.
- `sqlc generate` completed from the single source query file.
- Focused tests passed against the developer PostgreSQL database; full
  regression remains the next verification step.
