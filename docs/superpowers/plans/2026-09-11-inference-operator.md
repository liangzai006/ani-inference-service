# Inference Operator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an independent Inference gRPC control plane with PostgreSQL durability, Kubernetes CRD/Operator reconciliation, quota reservation integration, audit history, and restart/watch-loss recovery.

**Architecture:** PostgreSQL is the business authority for desired state, generations, operations, quota reservations, observations, audits, and durable work. An `InferenceService` CRD is the Kubernetes desired-state projection; controller-runtime reconciles only owned resources while startup scans and database due work guarantee recovery.

**Tech Stack:** Go, pinned ani-kratos-layout `fd18422211c741dd5242d2992c3d307d522aa28c`, Kratos gRPC transport, PostgreSQL, sqlc, controller-runtime, client-go, Kubernetes CRD.

## Global Constraints

- Modify only `/root/kubercon/ani-inference-service`; `/root/kubercon/ANI` is read-only.
- Do not use ani-core-platform or copy old ANI REST/OpenAPI/Proto/Core PlatformWorkload/Observer/Reconciler.
- Use PostgreSQL + sqlc, no RLS, explicit tenant_id in every tenant query and composite relation.
- Use Kubernetes ResourceRequirements maps for requests/limits; no separate Accelerator field; replicas=1 in the first slice.
- Do not operate a cluster, configure a remote repository, commit, or push.

### Task 1: Design and source provenance

**Files:**
- Create: `docs/superpowers/specs/2026-09-11-inference-operator-design.md`
- Create: `docs/superpowers/plans/2026-09-11-inference-operator.md`
- Modify: `docs/execution/status.md` after verification evidence exists.

- [x] Record authority boundaries, gRPC ResourceSpec, operation sequence, quota and audit contracts, recovery rules, and validation scope.
- [x] Record fixed layout SHA, generator command, generated tree and diff before implementation.
- [x] Review the design for contradictions, placeholders, and missing ownership rules.

### Task 2: Generate the independent layout skeleton

**Files:**
- Create: `cmd/ani-inference-service/`
- Create: `api/inference/v1/`
- Create: `api/inference/v1/` (business and configuration Proto sources and generated code)
- Create: `internal/server/`
- Create: `configs/`
- Create: `go.mod`, `go.sum`, `Makefile` as produced or required by the pinned layout.

- [x] Run the pinned layout generator from a clean local source.
- [ ] Verify only the new repository is modified and no ANI runtime import or local replace is present.
- [ ] Start the formal process with layout gRPC/admin endpoints and record output.

### Task 3: Define the independent API and CRD

**Files:**
- Create: `api/inference/v1/inference.proto`
- Create: `config/crd/bases/ani.kubercloud.com_inferenceservices.yaml`
- Create: `internal/service/inference_service.go`

- [x] Define Create/Get/List/Update/Start/Stop/Restart/Delete and operation RPCs.
- [x] Define `ResourceSpec.requests` and `.limits` as `map<string,string>` and validate Kubernetes Quantity, including Kubernetes limits-only defaulting for extended resources and equality when both maps contain the key.
- [x] Define generation, asynchronous operation, tenant context, request_id, and error details.
- [x] Define CRD spec/status without making status the business authority.

### Task 4: Implement schema, sqlc and repositories

**Files:**
- Modify: `migrations/000001_inference_control_plane.sql`
- Create: `sqlc.yaml`
- Create: `queries/inference.sql`
- Create: `internal/data/postgres/`

- [x] Correct tenant-scoped keys and model runtime resources as a set when multiple object kinds are enabled.
- [x] Add operation step, quota reservation, audit event, lease/CAS, and durable-work constraints.
- [x] Generate sqlc code from the single migration/query source.
- [x] Add real PostgreSQL tests for migration replay, tenant isolation, FK behavior, idempotency races, lease expiry, and generation CAS.

### Task 5: Implement biz use cases and gRPC admission

**Files:**
- Create: `internal/biz/`
- Modify: `internal/service/inference_service.go`
- Modify: `cmd/ani-inference-service/main.go`

- [x] Implement local transaction for service/spec/operation/work/audit.
- [ ] Implement idempotency by tenant + method + request_id and expected-generation CAS.
- [ ] Implement quota adapter calls with reservation_id and retryable operation steps.
- [ ] Return resource projection + operation_id without waiting for runtime readiness.

### Task 6: Implement controller-runtime and durable recovery

**Files:**
- Create: `internal/data/kubernetes/`
- Create: `internal/server/manager.go`
- Create: `internal/data/worker/`

- [ ] Watch InferenceService, Deployment, Service, Pod and map events to stable service keys.
- [x] Claim durable PG work with lease token, re-read current generation, render/apply deterministic resources, and write CAS-protected results.
- [ ] Add startup scan, due scan, retry/backoff, cache-sync handling, and bounded shutdown.
- [ ] Reject UID/resourceVersion ownership conflicts and never adopt same-name foreign objects.
- [ ] Add the official LeaderWorkerSet API dependency, typed LWS renderer, group/worker observation and distributed readiness tests; refuse LWS when the cluster/API is unavailable.

### Task 7: Implement runtime, quota, publication and audit behavior

**Files:**
- Create: `internal/data/publication/`
- Create: `internal/data/quota/`
- Modify: `internal/biz/`
- Modify: `internal/data/postgres/`

- [ ] Separate Pod Ready, model ready, publication effective, invocation health, and operation success.
- [ ] Implement publication withdrawal before stop/restart/delete and quota release only after runtime disappearance.
- [ ] Append immutable audit events for lifecycle, quota and failure transitions.
- [ ] Keep quota total ledger external; store reservation reference/state locally.

### Task 8: Verification and status evidence

**Files:**
- Create: `internal/**/**/*_test.go`
- Modify: `docs/execution/status.md`
- Create: `docs/execution/records/2026-09-11-implementation.md`

- [x] Run format, static checks, unit tests, sqlc generation check, and formal process checks.
- [x] Run real PostgreSQL replay and cross-tenant negative tests.
- [ ] Run envtest/controller tests for CRD, UID conflict, watch loss, restart recovery, and no-GET progress.
- [ ] Record real Kubernetes/GPU/data-plane/quota evidence separately as pass/fail/not_verified.
