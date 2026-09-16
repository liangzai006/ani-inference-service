# Progress

## 2026-09-12 official LWS controller handoff

- 目标仍未完成。typed LWS renderer 现在显式填充官方 webhook 的
  `RollingUpdateConfiguration` 默认值，避免 webhook-free 路径被官方 controller
  解引用空指针。
- 新增隔离 Kubernetes 1.36.2 envtest：直接使用生产 typed renderer，启动官方 LWS v0.10.0 controller，创建
  一个 group/3 workers 的 LWS，确认 controller 创建 StatefulSet；Pod、scheduler、
  GPU/device-plugin、多节点和真实集群行为仍为 `not_verified`。
- 本轮全量 `go test -p 1 ./... -count=1`（真实本机 PG + envtest）、`go vet`、
  `go build` 和 gofmt 均通过。证据见
  `docs/execution/records/2026-09-12-lws-controller-envtest.md`。

## 2026-09-12 audit context and runtime fencing continuation

- 目标仍未完成。异步 operation step/retry 审计现在从持久 acceptance 事件恢复
  Actor/RequestID，并原子记录 before/after phase、step、错误状态；真实本机 PG
  与领域回归通过。gRPC 提供 `WithActor` 注入点，正式 IAM 主体接线仍为
  `not_verified`。
- Deployment、endpoint Service、LWS 的 GET→SSA 并发 resourceVersion Conflict
  已在隔离 API server 通过；LWS renderer 的 RolloutStrategy schema 问题已修复。
  LWS controller/GPU/device-plugin/多节点调度、Job 生命周期和外部 provider 仍未验证。
- Create/Update/生命周期命令的 Actor 传递回归已补齐；开发机 PostgreSQL、全量
  Go 测试、vet、build 和 sqlc/buf 复验通过。

## 2026-09-12 initial status control identity

- 分类：有实现进展，目标仍未完成。复现并修复首次投影忽略持久 CR binding 的问题。
- 同一 PG 状态查询带回最新 control binding，按 namespace/name/UID 拒绝误写，
  tombstone 不返回投影；真实 PG 正反向回归已通过，不增加 migration 或新身份账本。
- CR spec apply 和 DeleteCR 的 UID/current-RV fencing 已修复；下一切片是 runtime
  对象 GET→SSA 窗口的真实 API Conflict 验证。记录见
  docs/execution/records/2026-09-12-status-control-identity.md。
- 最终同时启用 PG/envtest 的全量测试通过（Kubernetes 15.825s、PG 4.876s），
  vet/build 通过，sqlc 已生成，gofmt 无输出。
- CR fencing 切片通过 TDD：fake 先复现替代 UID 被 SSA 改写和 status 后旧 RV 阻止
  删除，修复后真实 Kubernetes 1.36.2 envtest 验证两者均安全收敛。runtime 对象
  GET→apply 现在携带并发 RV，Deployment/Service/LWS 的真实 API Conflict 回归通过。
- 本轮修改后的全量 PG/envtest `go test -p 1 ./... -count=1`、vet、build 通过。
- 官方 LWS CRD 已加入 fencing envtest；Deployment、endpoint Service、LWS 的
  GET→SSA 并发修改均返回 Conflict。测试发现并修复 typed LWS renderer 缺失
  `RolloutStrategy=RollingUpdate` 的真实 schema 问题。LWS controller/GPU/调度仍未验证。

## 2026-09-12 status concurrency verified

- 分类：本轮有进展，新增回归证据与修复；目标仍未完成。
- 隔离 API server 复现旧 status patch 覆盖新状态和同名重建对象；使用 SDK
  optimistic lock、APIReader 重读和调用期间 UID/租户检查修复。
- PG status source 改为单条 sqlc SELECT，保留旧发布事实且限制新代 effective；
  staleAfter、当前 operation 和租户隔离有真实 PG 证据。
- 同时开启真实 PG 和 envtest 的全量 Go 测试通过；首次投影的持久 CR UID
  校验仍未实现。详情见 docs/execution/records/2026-09-12-status-concurrency.md。

## 2026-09-12 contract and Job wake-up

- 补齐 gRPC Create/Update/Read 的 `served_model_name`，Update clone 在非空时覆盖上一代，读投影和集成断言均保留别名。
- controller-runtime 注册 batch/v1 Job watch；Job 事件只触发 durable work，不把 Kubernetes 事件当作任务队列。
- `buf generate`、`sqlc generate` 和 focused PG/service tests 通过。

## 2026-09-12 status freshness and operation listing

- `GetRuntime`/status projection 现在持久化读取 `stale_after`，避免 CR status 丢失观察时效。
- status-only patch 遇 resourceVersion 冲突会用 APIReader 和最新 PostgreSQL projection 重试。
- 新增 tenant-scoped `ListOperations` gRPC、resource 过滤和 keyset page token。
- 全量 Go tests、vet、build 与 buf lint 通过；真实 API server status subresource、外部 provider、LWS/GPU 仍 `not_verified`。

## 2026-09-12 post-publication health and LWS correctness

- Reproduced and fixed LWS worker overcounting: leaders, terminating/non-running
  Pods, invalid indices and duplicate group/worker slots cannot fill missing workers.
- Endpoint Service now avoids the LWS-owned headless name and selects only leaders.
- Added durable verify_invocation after publication confirmation. Prepublication
  readiness no longer depends on an endpoint that is not yet published.
- Continuous observation now probes confirmed current-generation publications;
  explicit unknown overwrites old healthy, and health does not carry across generations.
- Applied 000011 atomically to the local isolated PostgreSQL database; sqlc generate,
  full tests with real PostgreSQL, vet and build passed. Real providers, controller/GPU,
  engine/data-plane and process crash recovery remain not_verified.

- 已读取用户指定的设计文档和 migration。
- 已完成方案审查与入口资源契约确认。
- 本轮已按实施计划落地独立服务骨架和首个可验证切片；仍未操作集群、远程仓库或提交代码。
- 已保存 `docs/superpowers/specs/2026-09-11-inference-operator-design.md` 与 `docs/superpowers/plans/2026-09-11-inference-operator.md`。
- 已导入固定 layout 的运行骨架到 `cmd/ani-inference-service`、`internal/server`；固定 SHA 的目录/生成链已在独立临时目录核对，Kratos 生命周期适配已接入。
- 已新增独立 `inference.v1` Proto、InferenceService CRD、sqlc 配置/查询、收敛后的 migration、资源校验、生命周期门禁、配额/审计接口和 durable worker 核心。
- Go 已升级到 1.26.8；`GOCACHE=/tmp/ani-go-cache go test ./internal/biz/...` 通过。
- 修正资源语义：普通资源保持 requests/limits 独立；扩展资源允许 limits-only，并按 Kubernetes 规则将其作为有效 request；两者同时出现时要求相等。
- 通过：临时独立 module 执行测试和 migration 静态断言；真实 PostgreSQL 通过开发机网络和授权的 `docker exec recycling-postgres` 完成 migration/集成测试。
- Go 1.26.8 下此前完整 `go test ./...` 与 `go build ./...` 曾通过；端口监听测试在受限沙箱中自动 skip，最新 fresh-cache 结果见下方限制。
- 用户提供的 Docker PostgreSQL 已通过授权 `docker exec` 验证；已创建独立 `ani_inference` 数据库并重放 migration。8 张表、无 RLS、跨租户复合 FK 负向测试通过。
- 已加入 typed `api/inference/crd/v1` `InferenceService` API（含生成式 DeepCopy/Object/List）并接入 controller-runtime CR watch/Reconcile；校验 tenant/service 所有权标签，并用周期 `RequeueAfter` 覆盖 Watch 丢失唤醒；对应单元测试通过。
- 已加入 PostgreSQL `CreateService` 本地事务 repository：原子写入 service/spec/operation/runtime/resource_work/配额 pending reservation/idempotency/audit；真实 `recycling-postgres/ani_inference` 集成测试覆盖首次写入和同 key/hash 幂等重放并通过。
- 已补充 `config/crd/bases/ani.kubercloud.com_inferenceservices.yaml`，声明 generation、modelVersionId、Kubernetes requests/limits、replicas=1 和分离的 runtime/publication/invocation status；PyYAML 结构断言通过。
- 已加入 PG-backed `WorkStore`：原子 `FOR UPDATE SKIP LOCKED` 领取租约，Commit/Retry 以 tenant/service/lease token/current generation CAS，`next_run_at` 支持无事件时周期观察；真实 PG 验证租约过期、旧 token、generation 变化拒绝和周期再次领取。
- Deployment renderer 已改为返回 typed `*appsv1.Deployment`，资源使用 `corev1.ResourceRequirements`/`resource.ParseQuantity`；selector 不再包含 generation，ownership labels 统一为 `ani.kubercloud.com/*`，并同时校验 tenant 与 service。
- controller-runtime scheme 现在同时注册 typed CRD、`apps/v1` Deployment 和 `core/v1` 类型，可直接交给 typed client 使用。
- gRPC Create 已完成租户上下文校验、Kubernetes Quantity 规范化、扩展资源 limits-only 默认 request、确定性 protobuf 请求哈希和异步 operation 返回；PostgreSQL adapter 已把命令映射到原子 CreateService 事务。`buildApp` 支持注入 use case，但生产 PG DSN/连接池仍待 composition root 接线。
- 资源量校验现在直接使用 Kubernetes 官方 `resource.ParseQuantity` 和资源名校验，避免手写单位解析与 SDK 语义漂移。
- 已补齐 tenant-scoped `GetInferenceService`/`GetOperation` 只读链路：sqlc 查询、PG adapter、gRPC 身份校验和 NotFound 映射；`ANI_DATABASE_DSN` 接入时由 composition root 注入 Create/Read use case。
- 已将官方 `sigs.k8s.io/lws v0.10.0`（开发机联网下载）接入，并用其 typed API 渲染 LWS：`replicas` 表示 group 数量，`worker_replicas` 表示每组 worker 数量；LWS 不可用时明确拒绝，不降级为 Deployment。真实集群 CRD/controller 验证仍待完成。

## Errors

- 受限 shell 的 `127.0.0.1:5432` 不可见，改用用户授权的 `docker exec recycling-postgres` 完成数据库验证。
- 第一次 FK 负向测试错误地在两个租户都插入了同一 service UUID；修正为仅在 tenant B 创建 service、tenant A 复用其 UUID 后，负向测试通过。
- Go 已升级后重新运行 `sqlc generate` 成功，生成 `internal/data/postgres/{db.go,models.go,inference.sql.go}`；新增 `scripts/migrate-local.sh`，使用调用方提供的 PG 凭据创建 `ani_inference` 并重放唯一 migration。
- 新增 `internal/data/kubernetes/renderer.go`，将已校验的 ResourceSpec 渲染为带 service/tenant/generation 归属标签的 typed Deployment；controller-runtime CR watch/Reconcile 适配已实现。
- 按用户要求移除 `internal/conf/v1` Proto 源和生成目录，将配置 Proto 与业务 Proto、生成代码统一迁移到 `api/inference/v1`；`buf.yaml`/`buf.gen.yaml` 和 Kratos 入口引用已同步。
- 使用开发机网络的 Buf 1.50.0 和 protoc-gen-go v1.36.11 重新生成 API/config protobuf；`buf lint`（仓库明确的 MINIMAL 规则）通过。
- controller-runtime 的 CR/Deployment/Service/Pod/LWS 事件映射、durable PG worker、lease/CAS observation 和 operation runner 已实现；真实 publication/quota/model/engine provider 仍未接入。
- 最新全量测试、`go vet -p 1 ./...`、`go build -p 1 ./...` 和本机 PostgreSQL 包测试均通过。
- `controller-gen v0.11.1` 在 Go 1.26 下执行 object 生成时触发其旧 loader 的 panic，未把该旧工具加入 go.mod；现有 DeepCopy 文件保持 controller-gen 兼容格式，后续应锁定与 Go/Kubernetes 版本匹配的新 controller-tools 后重新生成。
- 为与 gRPC 仅要求 `model_version_id`、且 Model 由外部领域拥有保持一致，已将 `inference_specs.model_id` 改为可选外部引用；本地 `ani_inference` 已执行 `ALTER COLUMN model_id DROP NOT NULL`，未写入伪 UUID。
- Get/List 现在完整投影 inference spec 中的 artifact/provider/reference/SHA、engine type/image 和 command argv；新增真实 PG 集成测试与记录 `docs/execution/records/2026-09-12-read-projection.md`。
- 修复 update/restart 在 runtime absence 后错误跳过 `release_previous_quota` 的状态机路径；新增顺序测试与记录 `docs/execution/records/2026-09-12-update-quota-order.md`。
- Service 端口/引擎 listener 现在要求显式 endpoint 合同并持久化；没有 endpoint 时不创建 Service。真实 Service API、publication 地址确认和 Job runtime apply 仍保持 `not_verified`。
- 新增 `materialize_model` 持久 operation step（migration 000007）、显式 `ModelPort` 和 lease-fenced `model_ready` 写回；真实 Model/Storage artifact provider 仍未接入，未宣称物化完成。
- 物化开始时会用同一 resource-work lease 清除上一代 `model_ready`，并以真实 PG 集成测试证明旧就绪事实不会残留到新 generation。

## 2026-09-12 durable recovery verification

- Added `TestWorkStoreRecoverRecreatesMissingWorkIntegration`: with a durable service and no `inference_resource_work` row, `WorkStore.Recover` recreates the row and PostgreSQL reclaims it.
- Developer-machine PostgreSQL evidence passes together with lease-expiry fencing (`TestWorkStoreLeaseAndObservationIntegration`).
- Identified readiness follow-up: when background servers are present, cache/provider readiness must explicitly transition to ready after dependency gates; current `AfterStart` only records the initial false state.

## 2026-09-12 readiness gate

- Added `LoopServer` dependency-gated `OnReady` callback and `ReadyCheck`.
- Connected background worker readiness to the application `/readyz` state without allowing `AfterStart` to overwrite asynchronous readiness.
- Main Kubernetes composition keeps the gate closed until admission, model, quota, publication, runtime, and audit providers are all wired. Async step audit now restores actor/request context from the durable acceptance event and records before/after state; IAM actor injection remains unverified.
- Server/app tests, full Go tests, vet, build, buf lint, and local PostgreSQL recovery tests pass.

## 2026-09-12 create lifecycle state-machine test

- Added a full fake-provider create lifecycle test that advances persisted steps from admission through publication and verifies quota, model, runtime, invocation, and publication gates.
- This is unit-level state-machine evidence; real provider/Kubernetes/engine/data-plane verification remains not_verified.
- Full Go tests, vet, build, buf lint, and local PostgreSQL recovery regression tests pass.

## 2026-09-12 real PostgreSQL lifecycle corrections

- A real repository/worker/runner/store lifecycle test reproduced quota resource loss: nested requests/limits were decoded into a flat string map and the error was ignored. The shared quota snapshot now uses resources.Spec, validates quantities, and rejects malformed data.
- The same lifecycle exposed GET/LIST reporting withdrawn after confirmed publication. Both now read the latest publication table record bounded by desired generation.
- Watch notifications now increment an independent durable sequence, so same-generation notifications during a lease survive acknowledgement. No new event returns work to its ordinary observation interval.
- Recovery integration now exercises the tenant repair shared by startup Recover, rather than notifying unrelated developer-database tenants. This is not an operating-system restart or real Kubernetes proof.
- See docs/execution/records/2026-09-12-pg-lifecycle-corrections.md for failures, fixes and evidence. Cross-generation publication, total quota sizing for replicas/LWS, and real external adapters remain outstanding.
- Final developer-machine full Go test run with the real PostgreSQL DSN passed (PG 2.086s, command/process 38.508s); vet, build and buf lint passed. Test teardown now checks errors and runs before the pool closes, scoped to generated fixture tenants.

## 2026-09-12 replacement and deletion lifecycle

- Real PostgreSQL create→update→restart→stop→start→stop→delete testing reproduced and fixed old-generation republishing, empty quota release after stop, and work acknowledgement rejection after a delete tombstone.
- The same sequence passes with injected interruption after quota release or new-generation publication is persisted, before terminal operation transition; historical publications remain withdrawn and persisted releases are not repeated.
- Developer-machine full Go tests with PostgreSQL passed (PG 3.505s, process 4.405s), as did vet and build; sqlc regenerated, no DDL or Proto changed.
- Evidence and remaining limits: docs/execution/records/2026-09-12-replacement-lifecycle.md. External providers remain fixtures; real process crash recovery and actual Kubernetes/quota/model/data-plane integration remain not_verified.

## 2026-09-12 quota aggregate demand

- Added `resources.Aggregate`: Kubernetes `Quantity` based demand calculation keeps the per-container requests/limits snapshot and derives units for Deployment replicas or LWS groups including leader plus workers.
- `quota.Reservation` now carries this derived demand for the future authority adapter; it is calculated from the immutable PostgreSQL spec and is not a second quota ledger.
- Focused PostgreSQL demand assertion and final full regression passed with the developer DSN; vet and build passed. A default sandbox cache failure was discarded and explicitly rerun with `/tmp/ani-go-cache`.
- The replacement lifecycle now switches to LWS (one group, three workers) and confirms the quota adapter receives four aggregate units after the initial one-unit Deployment; evidence is in `docs/execution/records/2026-09-12-quota-aggregate-demand.md`.
- Final full regression after this slice passed with PostgreSQL (command 2.879s, PostgreSQL 3.593s); vet and build passed.

## 2026-09-12 runtime binding boundary

- Reproduced a crash window where runtime apply succeeded but UID/resourceVersion binding waited for the later observation step; a retry could then see an existing object without a durable identity.
- Added a narrow binding writer contract. Typed Deployment/LWS apply now performs an uncached APIReader read and immediately persists the binding through PostgreSQL CAS; the main composition injects the repository adapter.
- Focused Kubernetes binding test, Kubernetes/PostgreSQL package tests, and the developer-machine full Go regression passed. Real API server and process crash recovery remain `not_verified`; evidence: `docs/execution/records/2026-09-12-runtime-binding-boundary.md`.
- Added a failure-path test proving a binding persistence error is returned after the Kubernetes apply and cannot be swallowed as success; the focused test passes.

## 2026-09-12 publication and generation fencing

- Fixed publication persistence so `publishing`/`withdrawing` record desired intent while preserving the last provider-confirmed observed phase; only `published`/`withdrawn` confirmation advances observed state.
- Added a PostgreSQL repository fence requiring every observed runtime binding generation to equal the claimed work generation.
- Real PostgreSQL focused tests and `sqlc generate` passed. Evidence: `docs/execution/records/2026-09-12-publication-and-generation-fencing.md`.

## 2026-09-12 model readiness fact

- Added durable `model_ready_known` so unknown, confirmed ready, and confirmed not-ready model facts are distinct across restarts.
- Model materialization clears the fact; the model observation adapter sets it only for a known provider result; RuntimeSource and Kubernetes observation merge the persisted model fact without inferring it from Pod readiness.
- Applied migration `000008_model_readiness_fact.sql`; real PostgreSQL assertion, sqlc generation, full tests, vet, and build passed. Real Model/Storage provider and the runtime invocation probe remain `not_verified`; the HTTPProbe adapter has focused tests.

## 2026-09-12 LWS worker readiness

- LWS observation now lists typed Pods and counts ready workers with the official LWS `WorkerIndexLabelKey`; `ReadyReplicas` remains group readiness.
- Runtime readiness requires controller generation/updated groups, ready groups, and all expected worker Pods. Added tests for both complete worker readiness and missing workers.
- Full tests, vet, and build passed. Real LWS controller scheduling and GPU behavior remain `not_verified`.

## 2026-09-12 CR control binding

- Added `InferenceService/control` to the tenant-scoped CAS binding relation. CR apply now persists server UID/resourceVersion immediately through the uncached APIReader.
- Existing same-name CRs without a durable control binding are rejected; DeleteCR requires the persisted control identity and Kubernetes UID/resourceVersion preconditions.
- Applied migration `000009_inference_cr_binding.sql`; real PostgreSQL binding integration, full tests, vet, and build passed. Evidence: `docs/execution/records/2026-09-12-cr-control-binding.md`.

## 2026-09-12 admission wiring

- Added a PostgreSQL-backed local admission adapter and injected it into the operation runner. It validates persisted runtime shape before quota/runtime calls without taking over IAM or Core governance.
- Added valid/invalid runtime shape tests; full tests, vet, and build passed. Evidence: `docs/execution/records/2026-09-12-admission-wiring.md`.

## 2026-09-12 runtime boundary follow-up

- Confirmed CR control binding and local admission wiring remain separate from invocation health; no runtime-ready shortcut was added for invocation checks.
- Invocation probe/publication data-plane contracts remain explicitly `not_verified`; evidence is recorded in `docs/execution/records/2026-09-12-invocation-and-cr-boundaries.md`.

## 2026-09-12 invocation probe boundary

- Added an explicit `InvocationProbe` port to the Kubernetes executor. Runtime readiness, persisted model readiness, and invocation health remain separate facts; an absent probe returns unknown and cannot authorize publication.
- Added a focused probe composition test; full tests, vet, and build passed. Real data-plane probe and endpoint contract remain `not_verified`.

## 2026-09-12 invocation readiness gate

- Main-process business readiness now requires an explicit `InvocationProbe`; a configured Kubernetes runtime adapter alone cannot advertise callable inference.
- Added a focused configuration test and passed Kubernetes/cmd tests. Real endpoint, probe protocol, and engine health remain `not_verified`.

## 2026-09-12 controller-runtime envtest watch

- Added an isolated envtest integration test with `UseExistingCluster=false`; it loads the InferenceService and official LWS CRDs and verifies CR create plus Deployment create/delete events reach the durable notifier.
- Kubernetes 1.36.2 API server/etcd envtest passed. LWS controller scheduling, PostgreSQL persistence, runtime status, GPU, and publication remain `not_verified`.

## 2026-09-12 applied generation projection

- Added migration `000010_applied_generation.sql`; service-level applied generation advances only after generation/lease-fenced observation CAS commits in the same PostgreSQL transaction.
- Wrapped the non-lease observation path in the same local transaction so observation and applied projection cannot be split by a process crash.
- Added `applied_generation` as a backward-compatible read-only field in the v1 gRPC resource; GET/LIST project the tenant-scoped PostgreSQL value.
- Real PostgreSQL lifecycle test, sqlc generation, full tests, vet, and build passed. Evidence: `docs/execution/records/2026-09-12-applied-generation.md`.

## 2026-09-12 HTTP invocation probe adapter

- Added an explicit endpoint-resolver based HTTP probe with separate known/healthy results and a bounded default timeout; it is not wired until Service/publication endpoint contracts are frozen.
- Development-machine httptest passed for 2xx, non-2xx, transport failure, and missing resolver. Real engine/data-plane health remains `not_verified`.

## 2026-09-12 typed Service endpoint renderer

- Added a typed Kubernetes Service renderer and fenced Service apply/binding with explicit container/service/target
  ports and protocol, plus a generation-specific selector; Deployment and LWS use
  typed `ContainerPort` when configured. No default listener port is inferred.
- Renderer, PG persistence, Service apply/binding tests and full Go test/vet/build passed. Publication endpoint confirmation, TLS and data-plane
  contracts remain `not_verified`.

## 2026-09-12 endpoint persistence and fenced Service apply

- Added optional `EndpointSpec` to gRPC/CRD and four nullable `inference_specs` columns in migration `000012`; sqlc clone/read/list paths preserve numeric or named targetPort and omit all columns when endpoint is absent.
- RuntimeSource restores the endpoint, and RuntimeExecutor applies the typed Service after runtime apply, records a separate `endpoint` binding, and includes it in fenced deletion/absence observation. LWS endpoint selectors target leaders only.
- Development-machine PostgreSQL integration, Buf lint/generate, full Go test, vet and build passed. Real Kubernetes Service API and publication/data-plane confirmation remain `not_verified`.
- Continuous runtime observation now validates the persisted endpoint Service binding when an endpoint is configured; a missing endpoint binding/service is recorded as degraded and cannot masquerade as runtime ready.

## 2026-09-12 quota reservation recovery

- `StepReserveQuota` now validates the durable reservation identity before provider calls. `confirmed` advances directly; `reserved` retries only `Confirm`; only pending/unknown local states call `Reserve`.
- Added a focused test proving a transient confirmation failure followed by worker retry performs one Reserve and two Confirm calls, then persists `confirmed`.
- The external quota contract still must define idempotent keys, `Confirm/Release` semantics, and a query-by-reservation recovery operation; Inference keeps no quota ledger.

## 2026-09-12 quota error fact

- Durable quota reservation writes now preserve `last_error_code` through the OperationStore adapter. Confirm and Release failures record stable local diagnostic codes (`quota_confirm_failed`, `quota_release_failed`); successful persistence clears the code.
- This remains a local failure fact only. Provider error taxonomy and query-by-reservation recovery still belong to the external quota authority contract.

## 2026-09-12 quota authority lookup recovery

- Added explicit `quota.ErrReservationNotFound`. During `pending` recovery, the worker queries the authority by reservation identity first; a found `reserved/confirmed` result is resumed, only an explicit not-found permits a new Reserve, and all other lookup failures retry without guessing.
- Added focused coverage for recovery from a remote `reserved` result with zero additional Reserve calls. Full PG-backed tests, vet, and build passed.

## 2026-09-12 quota confirmed recovery persistence

- Fixed the remote-lookup recovery path so a provider-returned `confirmed` reservation is written to PostgreSQL with the current lease/fence before the operation advances to `apply_cr`.
- Added a regression test proving the next step can reload `confirmed` state instead of incorrectly rejecting runtime apply.
- 2026-09-12: Added a tenant-scoped PostgreSQL `StatusProjectionSource` and
  controller-runtime status-only projector. CRD `status.observedGeneration`
  now maps to durable `applied_generation`; fake-client and real PostgreSQL
  source tests pass. Real API-server status-subresource behavior remains
  `not_verified`. Low-cardinality worker backlog/retry/staleness metrics are
  still pending.
- 2026-09-12: Added low-cardinality OpenTelemetry worker counters for claims,
  empty scans, completions, retries and failures. Error labels are bounded
  (`stale_generation`, `lease_lost`, `context`, `dependency_or_domain`); no
  tenant/resource/request identifiers are emitted. Backlog depth, oldest work
  age and observation staleness were subsequently added as aggregate PG-backed
  gauges; model-load duration and dependency error breakdown remain pending.
- 2026-09-12: Added sqlc-backed aggregate `WorkStore.MetricsSnapshot` and
  observability gauges for due work, oldest due age and stale observations;
  the Kratos app wires the durable source from `LoopServer` without high-cardinality
  labels. Model materialization duration and bounded dependency error counters were
  added around `ModelPort.EnsureModel`; real provider taxonomy and end-to-end timing
  remain `not_verified` (see `docs/execution/records/2026-09-12-model-materialization-metrics.md`).
