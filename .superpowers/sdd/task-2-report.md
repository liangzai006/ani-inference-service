# Task 2 report

- Added normalized Model capability snapshots: trim whitespace, lowercase, drop empty values, and deduplicate while preserving order.
- Propagated capabilities through Model create/update admission into durable create/update inputs.
- Persisted create capabilities under `spec_json.model_capabilities`.
- Update snapshots merge the capability field into existing JSON only when switching `model_version_id`; updates without a version keep the previous snapshot.
- Existing engine and command handling is unchanged.

Verification:

- Red phase: focused tests failed before implementation because `Snapshot.Capabilities` and `CreateInput.ModelCapabilities` did not exist.
- `go test ./internal/data/model ./internal/data/postgres ./internal/service`
- `go test ./internal/biz/inference ./internal/data/model ./internal/data/postgres ./internal/service`

The PostgreSQL integration tests were not run because `INFERENCE_PG_DSN` is not configured; the offline create durable-input capture test remains enabled.
