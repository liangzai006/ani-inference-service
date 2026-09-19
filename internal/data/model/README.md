# Model v1 client

This adapter owns the Inference side of the versioned Model gRPC contract.
`GetReadyVersion(ctx, tenant, modelVersionID)` returns a deployment snapshot.
Operation IDs and service IDs are not model version IDs. Catalog readiness is
not an Inference materialization or runtime readiness observation.

With `ANI_MODEL_GRPC_ADDR` configured, the composition root wraps the PostgreSQL
create use case with the Model lookup. It persists authoritative artifact
reference/SHA256 and Model engine/command defaults in Inference's own spec.
`artifact_provider=model` identifies Model as the authority for obtaining a
download URL; it does not infer a storage backend from an object key.
Only omitted or identical engine settings are accepted until an override policy
is specified. Signed download URLs are acquired on demand and never persisted.
Client errors retain their gRPC status, and failed Model queries do not create
an Inference aggregate. The optional materializer turns the authoritative
artifact into verified runtime files before Kubernetes applies the model
runtime; Kubernetes startup/readiness probes report runtime availability.

TLS is required by default (minimum TLS 1.3, system trust roots);
`ANI_MODEL_GRPC_SERVER_NAME` optionally supplies the server name. No production
Principal is fabricated; IAM/Gateway integration remains an external
deployment concern.

The wire contract is a service-owned snapshot of Model's `model.v1` API, with
only `go_package` adjusted. Regenerate from the Inference repository with Buf
1.60.0 and the pinned plugin versions in `buf.model.gen.yaml`:

```sh
buf generate --template buf.model.gen.yaml --path api/model/v1/model.proto
go test ./internal/data/model -count=1
```
