# Readiness gate evidence

## Change

`LoopServer` now invokes an optional `OnReady` callback only after `WaitReady` succeeds and `ReadyCheck` (when configured) returns true. The application connects that callback to `/readyz`; `AfterStart` no longer resets asynchronous readiness to false. The Kubernetes composition requires all operation providers (admission, model, quota, publication, runtime, audit) before promoting business readiness.

## Evidence

```text
GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/server ./cmd/ani-inference-service -count=1
ok .../internal/server
ok .../cmd/ani-inference-service
```

Additional tests prove the callback waits for the dependency gate and does not fire when the domain gate is false. Full repository verification remains required after the next slice.

## Status

Source and local tests: `pass`. Real Kubernetes cache/provider readiness and production dependency wiring: `not_verified`.
