# Durable create lifecycle state-machine evidence

## Scope

The runner now has a complete fake-provider lifecycle test. It executes the persisted create steps one at a time and verifies that durable transitions occur in order before publication:

`admission → reserve_quota → apply_cr → materialize_model → apply_runtime → observe_runtime → publish`.

The test also requires runtime readiness, model readiness, invocation health, quota confirmation, and publication confirmation. It is a state-machine safety test only; it does not prove a real provider or Kubernetes deployment.

## Evidence

```text
GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/biz/inference -count=1
ok .../internal/biz/inference
```

Source: `internal/biz/inference/runner_test.go`, `TestRunnerCreateLifecycleAdvancesDurableStepsBeforePublish`.

## Status

Runner ordering and durable transition behavior: `pass` at source/unit level. Real quota, model, Kubernetes, engine, publication, and invocation providers: `not_verified`.
