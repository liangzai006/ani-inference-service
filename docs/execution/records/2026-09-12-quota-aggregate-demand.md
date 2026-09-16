# Quota aggregate demand

Inference keeps the immutable per-container Kubernetes resource snapshot in the
reservation (`requests` and `limits`) and derives a separate demand passed to a
future quota authority adapter:

- Deployment: `units = replicas`.
- LeaderWorkerSet: `units = replicas × (worker_replicas + 1)`; the leader is
  included because it runs the same inference container resources.
- CPU, memory and extended resources such as `nvidia.com/gpu` are multiplied
  with Kubernetes `resource.Quantity` arithmetic. No GPU-specific fallback or
  local quota ledger is introduced.

`OperationStore.CurrentOperation` derives this demand from the immutable spec,
including the latest spec for stop/delete generations that do not create a new
spec. The reservation itself remains the durable local projection; Core remains
the quota policy and accounting authority.

## Verification

- Resource-level tests pass for Deployment and LWS quantities, invalid runtime
  shapes and quantity precision errors.
- Real PostgreSQL lifecycle tests pass for create → update (switching to one
  LWS group with three workers) → restart → stop → start → stop → delete. The
  fake provider receives one unit for the initial Deployment and four units for
  subsequent LWS generations, while PostgreSQL preserves the raw snapshots.
- Full Go tests, vet and build pass after this change.

This proves local demand derivation and persistence boundaries only. Core quota
Reserve/Confirm/Release RPCs, tenant policy, external accounting, retries and
compensation still require a fixed versioned authority contract and remain
`not_verified`.
