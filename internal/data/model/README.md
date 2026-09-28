# Model v1 client

This adapter owns the Inference side of the versioned Model gRPC contract.
`GetReadyVersion(ctx, tenant, modelVersionID)` returns a deployment snapshot.
Operation IDs and service IDs are not model version IDs. Catalog readiness is
not an Inference materialization or runtime readiness observation.

With `ANI_MODEL_GRPC_ADDR` configured, the composition root wraps the PostgreSQL
create use case with the Model lookup. It persists the authoritative artifact
reference/SHA256 in Inference's own spec. The request must provide the engine
type, image, and complete startup command; Model engine/command fields are
compatibility metadata and are never used as defaults. The request's engine
values are persisted and passed to the Kubernetes runtime unchanged.
`artifact_provider=model` identifies Model
as the authority for obtaining a download URL; it does not infer a storage
backend from an object key. Signed download URLs are acquired on demand and
never persisted.
Client errors retain their gRPC status, and failed Model queries do not create
	an Inference aggregate. The optional materializer turns the authoritative
	artifact into verified runtime files before Kubernetes applies the model
	runtime; Kubernetes startup/readiness probes report runtime availability. The
	fetch Job accepts HTTP or HTTPS and receives the Model version's recorded
	artifact size as its download/extraction limit, so the old fixed 512 MiB
	ceiling is not used.

The new-cluster deployment uses plaintext Model gRPC inside the cluster. Model
RPCs take the tenant ID from the request and do not require a trusted Principal,
TLS certificate, or `ANI_MODEL_GRPC_SERVER_NAME`.

The wire contract is a service-owned snapshot of Model's `model.v1` API, with
only `go_package` adjusted. Regenerate from the Inference repository with Buf
1.60.0 and the pinned plugin versions in `buf.model.gen.yaml`:

```sh
buf generate --template buf.model.gen.yaml --path api/model/v1/model.proto
go test ./internal/data/model -count=1
```
