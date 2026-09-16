# Manager composition slice — 2026-09-11

## Scope

This slice adds the process lifecycle boundary for the independent Inference
operator. It does not load a Kubernetes configuration implicitly, start a
cluster manager, or change a cluster.

## Implemented

- `internal/data/kubernetes.NewManager` requires an explicit `*rest.Config`,
  registers the typed InferenceService, core, apps and LWS schemes, and calls
  `Controller.SetupWithManager` before returning an unstarted manager.
- Controller-runtime metrics, health probe and pprof listeners default to
  disabled because the Kratos admin server owns process endpoints. Callers can
  opt in through `manager.Options`.
- `internal/server.ManagerServer` adapts the manager's blocking `Start` method
  to the Kratos server lifecycle. It uses a child context, propagates startup
  errors, and cancels and waits during `Stop`.
- `internal/server.NewLoopServer` composes a durable `work.Store`, tenant
  source and executor into `work.Loop` without introducing an in-memory queue
  or starting goroutines during construction.

## Evidence

`GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/server ./internal/data/kubernetes`
and compile-only `go test -p 1 ./... -run '^$'` pass. The constructor tests do
not contact a Kubernetes API; real manager startup remains `not_verified` until
an explicitly isolated Kubernetes/LWS environment is authorized.

