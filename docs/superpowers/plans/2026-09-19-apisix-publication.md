# APISIX HTTPRoute Publication implementation plan

## Goal

Wire the production Publication port to Kubernetes Gateway API `HTTPRoute` objects so publish creates or updates the route, withdraw removes it, and confirmation waits for the APISIX controller status. The adapter must be deterministic per tenant/service/generation and fenced against stale operations.

## Steps

1. Reuse the existing fenced publication identity and load immutable backend endpoint facts from the desired runtime generation.
2. Add a controller-runtime Kubernetes adapter that server-side-applies and UID/resourceVersion-fenced deletes of an HTTPRoute, validates `Accepted=True` and `ResolvedRefs=True`, and returns the externally reachable URL.
3. Keep route facts derived from PostgreSQL's immutable inference spec and runtime namespace; publication persistence continues to be owned by the existing operation store.
4. Wire the adapter into `buildKubernetesServers` and configure Gateway/host/path through environment variables; the adapter uses the official typed `sigs.k8s.io/gateway-api/apis/v1` objects and does not replace the existing APISIX controller.
5. Add unit tests for deterministic names, stale-generation fencing, publish/withdraw idempotency, status confirmation, and endpoint construction.
6. Run formatting, focused tests, the full inference test suite, and push the implementation to the inference repository.

## Validation

- `go test -p 2 -count=1 ./...`
- `git diff --check`
- live APISIX smoke test remains the separate cluster acceptance gate; code completion does not claim the full create/stop/restart/update/delete acceptance until Model, Quota, IAM and a running inference deployment are available.
