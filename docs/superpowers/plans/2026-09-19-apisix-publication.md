# APISIX HTTPRoute Publication implementation plan

## Goal

Wire the production Publication port to Kubernetes Gateway API `HTTPRoute` objects so publish creates or updates the route, withdraw removes it, and confirmation waits for the APISIX controller status. The adapter must be deterministic per tenant/service/generation and fenced against stale operations.

## Steps

1. Extend the publication domain projection with the immutable route identity and backend endpoint facts loaded from the desired runtime generation.
2. Add a controller-runtime Kubernetes adapter that server-side-applies and UID/resourceVersion-fenced deletes of an HTTPRoute, validates `Accepted=True` and `ResolvedRefs=True`, and returns the externally reachable URL.
3. Load route facts from PostgreSQL's immutable inference spec and runtime namespace, preserving existing publication persistence and operation fencing.
4. Wire the adapter into `buildKubernetesServers`, configure Gateway/host/path through environment variables, and register the Gateway API unstructured type without replacing the existing APISIX controller.
5. Add unit tests for deterministic names, stale-generation fencing, publish/withdraw idempotency, status confirmation, and endpoint construction.
6. Run formatting, focused tests, the full inference test suite, and push the implementation to the inference repository.

## Validation

- `go test -p 2 -count=1 ./...`
- `git diff --check`
- live APISIX smoke test remains the separate cluster acceptance gate; code completion does not claim the full create/stop/restart/update/delete acceptance until Model, Quota, IAM and a running inference deployment are available.
