# Model v1 client

This adapter owns the Inference side of the versioned Model gRPC contract.
`GetReadyVersion(ctx, tenant, modelVersionID)` returns a deployment snapshot.
Operation IDs and service IDs are not model version IDs. Catalog readiness is
not an Inference materialization or runtime readiness observation.

With `ANI_MODEL_GRPC_ADDR` configured, the composition root wraps the PostgreSQL
create use case with the Model lookup. It persists the authoritative artifact
reference/SHA256 in Inference's own spec. The request must provide the engine
type, image, and startup command; Model engine fields are returned to callers
as model-version defaults, but this adapter does not overwrite the caller's
final values. The request's engine values are persisted and passed to the
Kubernetes runtime unchanged.
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

The new-cluster deployment uses mTLS for Model gRPC inside the cluster. Model
RPCs require the verified caller identity, tenant metadata, client certificate,
and the configured `ANI_MODEL_GRPC_SERVER_NAME`; the request tenant cannot
replace the trusted principal.

The wire contract is a service-owned snapshot of Model's `model.v1` API, with
only `go_package` adjusted. Regenerate from the Inference repository with Buf
1.60.0 and the pinned plugin versions in `buf.model.gen.yaml`:

```sh
buf generate --template buf.model.gen.yaml --path api/model/v1/model.proto
go test ./internal/data/model -count=1
```

## 中文字段约定

前端选择模型版本后，调用 Model/Governance 的 `GetModelVersion` 或
`ListModelVersions`，用返回值填充推理创建表单：

| ModelVersion 字段 | 推理创建字段 |
| --- | --- |
| `engine_type` | `engine.type` |
| `engine_image` | `engine.image` |
| `startup_command` | `engine.command` |

创建时仍提交完整的 `engine` 对象，因此用户可以临时修改镜像、引擎类型或命令。
Inference 要求三项最终值都非空，并按原字符串保存；不会拆分命令、追加参数或从
模型名称生成 `--served-model-name`。
