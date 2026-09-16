# 2026-09-12 Read projection verification

## Change

`GetService` and `ListServices` now project the persisted inference spec fields
that are accepted by the gRPC contract: model artifact provider/reference/SHA,
engine type/image, and custom command argv. The list query remains explicitly
tenant-scoped and joins the spec on the service's desired generation.

## Evidence

- `queries/inference.sql` was regenerated with sqlc after adding the projection
  columns.
- `internal/data/postgres/read_usecase.go` decodes command argv and populates
  `ModelArtifact` and `EngineSpec` in both read paths.
- PostgreSQL integration test `TestReadUseCaseProjectsArtifactAndEngineFields`
  verifies the end-to-end projection against the local `recycling-postgres`.
- `go test -p 1 ./... -count=1`, `go vet -p 1 ./...`, and `go build -p 1 ./...`
  pass. PostgreSQL integration tests pass with the authorized local DSN.

## Boundary

This verifies persistence and read projection only. Model catalog ownership,
artifact materialization, engine load health, and publication providers remain
external dependencies and are still `not_verified`.
