# 2026-09-12 Update quota release ordering

## Finding and correction

The operation state graph already declared `release_previous_quota` for
update/restart, but the runner advanced directly from `observe_absence` to
`reserve_quota`. That could temporarily hold both the old and new generation's
reservations. The runner now selects `release_previous_quota` for update and
restart, and only then advances to the new reservation step.

## Evidence

- `internal/biz/inference/runner.go` chooses the declared step after runtime
  absence based on operation kind.
- `TestRunnerReleasesPreviousQuotaBeforeUpdateReserve` covers the ordering.
- `go test -p 1 ./internal/biz/inference -count=1` passes.

The external quota provider and its authoritative accounting protocol remain
unconfigured; this change only enforces the Inference-side ordering gate.
