# Flat Inference Request Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Gov 对外的创建推理接口收敛为扁平业务参数，同时保留旧接口读取/迁移能力；让 Model 版本提供可回填的镜像和完整启动命令默认值；让 Gov 将最终值原样转给 Inference，由 Inference 统一创建 KServe 运行时。

**Architecture:** 前端选择 `model_version_id` 后读取 Model 版本的默认引擎配置并回填，用户可以在提交前修改镜像、引擎类型、完整 `command`、CPU、内存、副本数和可选 GPU 选择。Gov 使用现有创建入口的扁平请求，校验 Model 版本 Ready，并把扁平请求映射到 Inference。Model、Gov、Inference 的业务协议都只保留一个完整 `command` 字符串；只有 Kubernetes 容器对象这一层使用固定包装 `command=["/bin/sh","-c"]`、`args=[原始完整命令]`，不解析或拼接命令。Inference 持久化最终配置并通过 KServe 创建 Deployment 或原生 LWS；没有 GPU 时不调用 Accelerator，有 GPU 时只由 `cluster_id/pool_id/profile_id/profile_version` 生成底层 GPU 请求。

**Tech Stack:** Go, protobuf/Buf, Kratos gRPC/HTTP, PostgreSQL/sqlc, Kubernetes client-go, KServe/LWS typed APIs, existing Gov Model and Accelerator clients.

## Global Constraints

- 所有 Model/Gov/Inference 业务接口都使用单个完整命令字符串；不得新增或保留面向调用方的 `command[]`/`args[]`。
- 新的 Gov 创建接口只接受扁平字段；不得要求前端组装 `resource`、`runtime`、`engine` 嵌套对象。
- `command` 是一条完整字符串，例如 `exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct`。任何服务都不得拆分、拼接、补参数、改顺序或自动注入 `--served-model-name`、上下文长度、并行度等引擎参数。
- Kubernetes 数组只表示固定 shell 包装：`["/bin/sh","-c"]` 与 `[command]`；不把用户命令解析为多个参数。运行镜像必须包含 `/bin/sh`，调用方负责决定是否在命令中使用 `exec`。
- Model 版本的 `engine_image`、`engine_type`、完整启动命令是默认值；创建请求携带的最终值优先，Model 版本更新不会改变已创建推理服务。`startup_args` 只作为数据库历史列保留，不再出现在任何新业务协议中。
- GPU 公开字段只有 `cluster_id`、`pool_id`、`profile_id`、`profile_version`。Gov 负责按 Deployment/LWS 和副本数推导内部 `replicas`、`devices_per_replica`、`container_name`；前端不得传这些调度细节。
- `cpu`、`memory` 映射为 Kubernetes requests；不生成隐藏的 limits 或默认资源值。`replicas` 直接表示 Deployment 副本或 LWS 组数。
- 新建资源统一使用 KServe；旧 Deployment 代码仅用于读取、删除和迁移兼容。新建时 `runtime_mode` 缺省为 KServe Deployment，只有分布式运行时显式使用 `leader_worker_set`。
- 不引入 APISIX、IAM 或新的默认配额逻辑；不修改旧集群资源。真实集群部署和远程 push 在实现和验证完成后单独执行。

---

## Task 1: Extend Model version defaults without command synthesis

**Files:**

- Modify `/root/kubercon/ani-model-service/api/model/v1/model.proto`.
- Add `/root/kubercon/ani-model-service/migrations/000007_model_version_engine_image.sql`.
- Modify `/root/kubercon/ani-model-service/queries/model.sql`.
- Modify `/root/kubercon/ani-model-service/internal/biz/model/model.go`.
- Modify `/root/kubercon/ani-model-service/internal/service/model.go`.
- Modify `/root/kubercon/ani-model-service/internal/data/postgres/version_store.go`.
- Update generated `api/model/v1/*.pb.go` and `internal/data/postgres/model.sql.go` with the repository generators.
- Update focused Model tests and fixtures that construct `ModelVersion` or `CreateModelVersionRequest`.

- [ ] Add `ModelVersion.engine_image` as field 16 and `CreateModelVersionRequest.engine_image` as field 15. Reserve the old `startup_args` protobuf field and remove it from the new Model request/response surface; the database column remains only for non-destructive history and is never combined with `startup_command`.
- [ ] Add a forward-only migration that adds `model_versions.engine_image TEXT NOT NULL DEFAULT ''`; do not rewrite existing rows or change shared tables.
- [ ] Include `engine_image` in all Model version SELECT/INSERT projections and sqlc parameters. Keep the SQL column name stable so existing migrations and readers remain compatible.
- [ ] Add `EngineImage string` to the domain version and service/storage mappings. New version creation requires image, engine type, and a non-empty full `startup_command` together; legacy rows may still read with an empty image and must be overridden explicitly by the Gov request. Do not add a domain path that joins the historical `startup_args` column.
- [ ] Ensure `GetModelVersion` returns image, engine type, and the unmodified full command string so the frontend can auto-fill them.
- [ ] Write tests first for: round-trip image persistence, rejection of a partial engine default, and exact preservation of `exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct`.
- [ ] Regenerate with the Model repository’s pinned workflow (`make generate` or the narrower repository generator when available), then run `make verify` and `go test ./...`.

**Commit checkpoint:** stage only Model proto, migration, query, generated output, domain/service/storage code, and related tests; commit as `model: expose engine image and full command defaults`.

## Task 2: Make Inference execute a raw command through one fixed wrapper

**Files:**

- Modify `/root/kubercon/ani-inference-service/internal/biz/inference/create.go` and `update.go`.
- Modify `/root/kubercon/ani-inference-service/internal/data/postgres/engine.go`, `queries/inference.sql`, and generated `internal/data/postgres/inference.sql.go`.
- Modify `/root/kubercon/ani-inference-service/internal/service/inference.go` and Model snapshot mapping in `internal/data/model/client.go` plus its tests.
- Modify `/root/kubercon/ani-inference-service/internal/data/kubernetes/kserve.go`, `renderer.go`, and `executor.go`.
- Update KServe, LWS, Deployment-compatibility, PostgreSQL, and service tests.

- [ ] Change the Inference `EngineSpec` business protobuf to a single `string command` field; reserve the old repeated command/args fields. Update every Model/Gov/Inference adapter and test to use the string field. Kubernetes rendering is the only place that creates the fixed wrapper arrays.
- [ ] Store the raw command as a string in the existing `command_argv` JSONB column (object form `{ "command": "<raw>" }`). Existing rows in the previously introduced wrapper-object form may be read only when they contain exactly `command=["/bin/sh","-c"]` and one raw `args` value; arbitrary old arrays are rejected as invalid instead of being joined.
- [ ] Update create/update validation so an explicit engine configuration requires image, type, and one non-empty raw command; an update that omits engine fields keeps the previous generation unchanged.
- [ ] Change KServe InferenceService, LLMI/LWS, Deployment compatibility, and executor rendering to emit exactly `command: ["/bin/sh", "-c"]` and `args: [raw]`. Do not add an `exec` prefix in code.
- [ ] Preserve the existing KServe-only create path and LWS worker rendering. The runtime mode determines only the Kubernetes resource shape; it must not alter the command text.
- [ ] Add tests asserting byte-for-byte command preservation, no `startup_args` concatenation, KServe shell wrapper output, LWS wrapper output, and legacy JSON read compatibility.
- [ ] Regenerate sqlc output with the repository’s pinned `sqlc` command, run `bash scripts/generate-api.sh lint` and `bash scripts/generate-api.sh` (the script mounts the locked Accelerator proto), then run `go test ./...`, `go vet ./...`, `go build ./...`, `git diff --check`, and `./charts/verify.sh`.

**Commit checkpoint:** stage only Inference command persistence/rendering and focused tests; commit as `inference: preserve full engine command through KServe wrapper`.

## Task 3: Add a flat Gov create contract while retaining one legacy route

**Files:**

- Modify `/root/kubercon/ani-governance/api/protos/inference/service/v1/owner.proto`.
- Modify `/root/kubercon/ani-governance/api/protos/admin/service/v1/i_inference.proto`.
- Modify `/root/kubercon/ani-governance/api/protos/catalog/service/v1/model.proto` so the Gov model-facing API also returns `engine_image` and reserves the historical `startup_args` field.
- Regenerate `api/gen/go/inference/service/v1`, `api/gen/go/admin/service/v1`, and OpenAPI output through `make api`.
- Modify `/root/kubercon/ani-governance/app/admin/service/internal/service/inference_service.go` and `cmd/server/wiring_ent.go`.
- Add/update `app/admin/service/internal/service/inference_service_test.go` and Model client contract tests.

- [ ] Replace the nested fields in the existing `CreateInferenceData` with flat fields. Keep the existing `CreateInference` RPC and route so clients have one stable endpoint; do not add a second “simple” route or preserve a nested engine/resource contract. The fields are exactly:

  ```proto
  string idempotency_key = 1;
  string name = 2;
  string model_version_id = 3;
  string served_model_name = 4;
  string engine_image = 5;
  string engine_type = 6;
  string command = 7;
  string cpu = 8;
  string memory = 9;
  int32 replicas = 10;
  GpuSelection gpu = 11;
  inference.v1.RuntimeMode runtime_mode = 12;
  int32 worker_replicas = 13;
  ```

  `GpuSelection` contains only `cluster_id`, `pool_id`, `profile_id`, and `profile_version`. The old nested request shape is intentionally removed from the public contract.
- [ ] Validate required identity, model version, name, served model name, image, engine type, raw command, positive replicas, and CPU/memory quantities. Reject partial GPU selection, `worker_replicas` for Deployment, and non-positive LWS worker counts. Do not add defaults for image, command, resources, or replicas beyond the explicitly documented runtime-mode default.
- [ ] Inject the already existing Gov `ModelClient` through a small interface (`GetModelVersion`) into `InferenceService`. On the simple path, call Model with the resolved tenant/actor and require a Ready version. Use the Model response for readiness and frontend/default verification; do not overwrite non-empty final request fields and do not build an artifact URL in Gov.
- [ ] Map `cpu` and `memory` to `inference.ResourceSpec.Requests`; leave limits unset. Map `runtime_mode`/`worker_replicas` to the internal `RuntimeSpec` and set the KServe provider only through the existing internal compatibility field.
- [ ] Map the caller’s raw command to the internal `EngineSpec.command` string without parsing it. Inference alone turns that string into the fixed Kubernetes wrapper. Pass the caller’s image, engine type, served model name, model version, replicas, and resources unchanged to Inference.
- [ ] For GPU requests, derive internal fields before quota/Accelerator admission: Deployment uses container `kserve-container` and GPU replicas equal inference replicas; LWS uses container `main` and GPU replicas equal `replicas * (worker_replicas + 1)`; `devices_per_replica` remains the existing single-device contract. No GPU selection means no Accelerator call.
- [ ] Keep the existing direct/no-GPU path and trusted owner call. Add tests with a fake Model client and fake Inference owner that assert exact forwarding, Model readiness rejection, CPU/memory mapping, no Accelerator call without GPU, and the two GPU derivations. Replace old nested-handler fixtures with flat request fixtures.
- [ ] Regenerate Gov code and OpenAPI with `make api`, run `make verify-ci`, and run the focused Gov package tests before the integration test.
- [ ] Regenerate the catalog model output from the updated source proto and update Gov model service mappings/tests so no Gov business API exposes `startup_args` or a command array.

**Commit checkpoint:** stage only Gov proto/generated/service/wiring/tests; commit as `governance: add flat inference create contract`.

## Task 4: Align read/list responses and frontend-facing documentation

**Files:**

- Modify `/root/kubercon/ani-governance/api/protos/inference/service/v1/owner.proto` and response mappings if the simple route currently exposes nested engine/resource fields.
- Modify the generated OpenAPI output through `make api`.
- Update `/root/kubercon/ani-governance/docs` or the repository’s existing API contract document, and `/root/kubercon/ani-inference-service/docs/deploy/inference` only where the new request contract is documented.
- Add example payloads under the existing API examples directory.

- [ ] Replace the Gov Get/List public response type that currently returns `inference.v1.InferenceService` with a flat `InferenceView` owned by the Gov contract. It must expose the resource identity/status fields needed by the UI plus `model_version_id`, `served_model_name`, `engine_image`, `engine_type`, raw `command`, `cpu`, `memory`, `replicas`, `runtime_mode`, `worker_replicas`, and the four-field `GpuSelection`; it must not expose `resource`, `runtime`, `engine`, `ModelArtifact`, accelerator attachments, or shell arrays. Keep the Inference-to-Inference internal client response unchanged.
- [ ] Map Get/List results from the internal Inference response into `InferenceView`, preserving status phases, generation, observed timestamp, and page token. Add focused tests for a string command and GPU view projection.
- [ ] Document that Model version values are UI defaults and the create request is authoritative. Document the exact command example and the `/bin/sh -c` image requirement. State explicitly that the backend never parses or appends command arguments.
- [ ] Document the KServe/LWS rules and GPU derivation, including the fact that no GPU field means no Accelerator admission.
- [ ] Add an OpenAPI example showing a temporary command override while keeping the same `model_version_id`.

## Task 5: End-to-end contract verification

**Files:**

- Add focused cross-service contract fixtures under existing test directories; do not add production code to test-only directories.
- Update the existing deployment/runbook documentation with the verification commands.

- [ ] Run Model unit/API tests and confirm a created version returns the image and raw command.
- [ ] Run Gov service tests with a fake Model and Inference server and confirm the full request is forwarded byte-for-byte.
- [ ] Run Inference renderer tests and inspect generated KServe objects for the fixed shell wrapper, CPU/memory requests, replicas, served model name, and LWS worker shape.
- [ ] Run the repository gates: Model `make verify`; Gov `make verify-ci`; Inference `bash scripts/generate-api.sh lint`, `bash scripts/generate-api.sh`, sqlc generation, `go test ./...`, `go vet ./...`, `go build ./...`, `git diff --check`, and `./charts/verify.sh`.
- [ ] If a live cluster is available, apply only the generated service changes with Helm/kubectl (never the test install script), create one CPU simple request, inspect the KServe resource and pod command, then delete it. GPU and multi-node verification remains blocked until a GPU-capable cluster is available.
- [ ] Record any unavailable external dependency (Model endpoint, KServe/LWS controller, Higress route, or GPU cluster) as `not_verified`; do not claim the full data-plane path passed without observed evidence.

**Commit checkpoint:** stage only integration fixtures and documentation; commit as `docs: document flat inference deployment flow` after all gates pass.

## Self-review checklist

- [ ] No new code splits a command string or appends engine flags.
- [ ] No public request exposes Kubernetes command/args arrays or accelerator scheduling internals.
- [ ] Model defaults are returned for UI prefill, while non-empty create request values always win.
- [ ] No-GPU creation bypasses Accelerator; GPU creation derives all internal GPU-only fields deterministically.
- [ ] KServe is the only new-resource provider; legacy Deployment code is not used for new creates.
- [ ] Generated protobuf/OpenAPI/sqlc files are regenerated by pinned repository workflows, not hand-edited.
- [ ] Tests cover the flat route and reject the removed nested command/args shape.

Plan complete and saved to `docs/superpowers/plans/2026-10-13-flat-inference-request.md`. Two execution options: 1. Subagent-Driven (recommended) — execute each task with fresh subagents and review checkpoints. 2. Inline Execution — execute the plan directly in this session. Which approach?
