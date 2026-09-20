# Inference 执行状态

更新日期：2026-09-12。本文件是本设计包唯一当前进度入口。

最新纠错：已修复 LWS worker 计数（排除 leader、终止/非 Running Pod，并按
group/worker 位置去重）；endpoint Service 使用独立的 `-endpoint` 名称且在
LWS 模式只选择 leader，避免占用 LWS controller 自有的 headless Service。
异步 operation 审计上下文现从持久 acceptance 事件恢复，step/retry 事件携带
Actor、RequestID 和 before/after 状态；IAM 可信主体接线仍未验证。
当前业务 operation 在发布确认后即可完成；Kubernetes startup/readiness probe
负责进程和模型运行状态。独立 invocation probe 属于可选的数据面运维检查，当前不
作为 Inference 业务门禁，也不接入正式进程。历史 `000011_invocation_verification.sql`
仍保留以兼容已应用数据库结构；`000012_inference_endpoint.sql` 已以单事务应用到
本机 `ani_inference`。
CRD manifest 也已同步 endpoint schema。配置 endpoint 时，持续观察会校验持久化 Service binding 的存在、UID、resourceVersion
和 generation；端点缺失会记录 degraded，不会把 runtime ready 直接当作可调用。
CR 投影现在也保留同一组显式 endpoint 端口和协议；未配置 endpoint 时不会写入默认端口。
配额 worker 对持久 `reserved/confirmed` 状态只做 Confirm 或直接推进，避免重启后重复 Reserve。
配额 Confirm/Release 失败的本地诊断码会持久到 reservation `last_error_code`，成功写回时清除。
pending 恢复会先按 reservation identity 查询 authority；只有明确 `ErrReservationNotFound` 才允许重新 Reserve。
durable work loop 现在首轮执行前用 `Recover` 唤醒持久 service，默认每分钟用
`Repair` 完整扫描 service 聚合并只补缺失 `resource_work`；恢复失败不会终止已有
worker，下一周期会重试，也不会因周期扫描递增已有 `dirty_version`。
当前源码已通过带真实本机 PostgreSQL 的全量 Go 测试（PG 包约 4s）、vet、build
和 sqlc generate。
开发机构建的正式 Kratos 进程已通过本机 `/healthz` 和优雅停机检查；这不替代
真实 Kubernetes worker、provider 或 operation crash 恢复证据。
CRD status projector 已用隔离 envtest 验证真实 status 子资源：SDK 乐观锁阻止
旧结果覆盖新状态；冲突重读时拒绝 UID 或 tenant/service 变化。PG source 已改为
单条 sqlc 查询，旧发布 phase 保留、effective 按 desired generation 判定，staleAfter
投影通过实际 PG 验证。完整测试同时开启 PG 与 envtest 已通过。首次投影对持久
control binding UID 的校验已补齐：同一语句读取最新有效 binding，拒绝身份不符和
tombstone，真实 PG 回归和隔离 API 重建场景通过；最终同时开启 PG/envtest 的全量
Go 测试、vet/build 通过，见[绑定身份记录](records/2026-09-12-status-control-identity.md)。
CR spec apply 已在 SSA 前核对持久 control UID，并使用当前 API RV；DeleteCR
不再比较 status 前保存的旧 RV，而是使用当前读取的 precondition。fake 与隔离
envtest 已通过；Deployment/Service/LWS apply 现在也携带当前 RV，真实 API server
并发 Conflict 回归通过。官方 LWS controller 创建 StatefulSet 的隔离 envtest 已
通过；真实 Pod/GPU 调度、LWS worker readiness 和多节点行为仍未验证，详见[LWS
controller 记录](records/2026-09-12-lws-controller-envtest.md)。
status 并发证据见[状态并发记录](records/2026-09-12-status-concurrency.md)。
gRPC/PG/CRD 已补齐 `served_model_name` 读写，controller-runtime 已注册 Job
runtime watch；Job watch 目前只负责唤醒 PostgreSQL 持久 work，Job apply/观察/binding
仍未接入，不能据此宣称 Job 生命周期完成。
已补齐 tenant-scoped `ListOperations` 及 keyset 分页；异步 step 审计已从持久
acceptance 事件恢复 actor/request 上下文并写入 before/after 状态，IAM 可信主体
注入和跨服务审计汇总仍未验证，详见[审计上下文记录](records/2026-09-12-audit-context.md)。
调用限流尚未在控制面实现，按责任表保留给推理数据面/Envoy，等待发布配置消费契约。
worker 已增加低基数 OTel 计数器：claim、空扫描、完成、重试和失败，error_class
仅使用固定类别；WorkStore 还提供 PG backlog、最老工作年龄和 observation stale
聚合 gauge，已由正式 observability 读取。`materialize_model` 还记录低基数的模型物化
时延直方图和依赖错误分类计数；真实 provider 的 taxonomy/端到端时延仍为
`not_verified`，见[模型物化指标记录](records/2026-09-12-model-materialization-metrics.md)。
验证详情及限制见[发布后健康与 LWS 纠错记录](records/2026-09-12-post-publication-health-and-lws.md)。

| 范围 | 当前状态 |
|---|---|
| 用户边界与模板/Network 来源核对 | 已完成只读源码核对，固定 SHA 见记录 |
| 独立设计包 INF-00 | 草案已迁入独立仓库并完成作者一致性检查；链接/格式检查 pass；待用户评审 |
| 独立数据库 migration | pass：`000001`–`000012` 已在 `recycling-postgres` 的 `ani_inference` 应用；Inference 自有表、LWS 字段、active operation 索引、runtime/control binding、publication/observation history 表、模型物化和 invocation operation step 约束、model readiness known fact、CR control binding、service-level applied generation、显式 endpoint nullable 列、无 RLS 和跨租户复合 FK 负向测试通过 |
| 独立阅读评审 | not_verified：两次尝试遇模型容量错误，未得到完成结论 |
| 本仓库服务生成、gRPC/PG/Controller 实现 | layout 骨架、`api/inference/v1` Proto/生成代码（含 ModelArtifact/EngineSpec）、typed CRD manifest、sqlc 输入/生成代码、领域资源/状态/durable worker、可复用 durable work loop、Deployment/LWS/显式 endpoint Service typed 渲染、PG lease/CAS WorkStore、tenant-scoped due/startup scan、runtime/LWS observation 字段和 desired-generation CAS、controller-runtime CR/runtime watch 唤醒适配（CR status 自触发过滤）、PG 只读 CRD status projector、Create/Get/GetOperation/List 只读 gRPC（Get/List 完整返回制品与引擎投影）、Start/Stop/Restart/Delete/Update 命令受理和 PostgreSQL Create/Read/Command/UpdateUseCase 适配（Update 保留/更新引擎与制品字段）、幂等重放返回持久 operation 状态、runtime binding UID/resourceVersion CAS、typed SSA CR/Deployment/LWS executor、LWS typed Pod worker readiness 统计、显式 durable `materialize_model` step、运行前配额/撤发布安全门禁、publication 意图先于 provider 调用持久化、删除前 CR/runtime 顺序、Kratos LoopServer/ManagerServer 生命周期适配、tenant/generation/phase/step/lease fenced operation CAS 和 durable retry、lease-fenced runtime observation/binding 写回及不可变 observation history、进程级 PG→Kubernetes composition、显式 quota/publication/runtime/admission/model operation runner、PG lease-fenced OperationStore、旧 generation publication/quota 的 fencing、running update 的上一代配额释放步骤、delete tombstone、主进程 active operation/terminal observation 选择和 RuntimePort 接线、后台组合未配置 provider 时的 readiness gate、LoopServer 依赖门禁后的 readiness 回调、本地 PostgreSQL-backed admission adapter 已实现；真实 quota/publication/admission/model provider 仍未配置 | 
| 本地 Go 测试/构建 | pass：开发机网络环境执行 `go test -p 1 ./... -count=1` 全量通过；`go vet -p 1 ./...`、`go build -p 1 ./...` 和全仓编译通过；runner fake-provider create 生命周期已验证 `admission → reserve_quota → apply_cr → materialize_model → apply_runtime → observe_runtime → publish → complete` 顺序；update/restart 在 runtime absence 后先释放上一代配额再预留新一代 |
| 真实 PG/K8s/引擎/数据面测试 | PG migration（含 LWS、binding/publication/observation 扩展和 operation step 扩展）、Create/生命周期命令/Update/List 本地事务、幂等重放、keyset 分页和跨租户隔离、Update spec clone 与上一代 reservation 发现、lease 过期/旧 token 拒绝、tenant-scoped due/startup scan、`Recover` 在 `resource_work` 丢失时按持久 service 聚合补回并重新领取、operation CAS/retry（含 resource_work lease）、lease-fenced observation/history、OperationStore、RuntimeSource 配额/发布事实读取、LWS observation 字段与 desired-generation/future-generation CAS、旧 generation publication/quota fencing SQL、controller Reconcile durable Notify 单测 pass；主进程 runner/RuntimePort 组合可编译但 quota/publication/admission/engine provider 未配置；真实 PG 跨代 create/update/restart/stop/start/delete 与结果落库后注入中断恢复 pass；真实 provider accounting/release、K8s API server/LWS、引擎/数据面仍 not_verified |
| 固定 layout 正式生成器 | pass：使用 pinned Kratos CLI/Buf 在 `/tmp` 独立目录生成并检查 module/tree；Buf lint 使用仓库明确的 MINIMAL 规则通过；未覆盖到当前仓库的业务文件 |
| 远程仓库、提交、推送、部署、切流 | 未执行、不在本轮设计范围 |

本仓库当前包含 layout 骨架、Proto/typed CRD、领域核心、sqlc 输入/生成代码、独立 migration、PG application 持久化切片、Create/Update/生命周期 gRPC 受理、durable work loop、typed CR/Deployment/LWS SSA 执行器、runtime/control binding fencing、CR/runtime apply 后经 uncached APIReader 立即持久化 binding、严格 observation binding generation fencing、lease-fenced observation/history 写回、独立 model_ready_known 持久事实、本地 PostgreSQL-backed admission 校验、operation step CAS/retry、publication desired/observed intent 分离、显式 operation runner/PG store 和 Kratos worker/manager 生命周期适配；其余真实 quota/publication/runtime provider 接线、模型物化、引擎调用和产品链路仍 not_verified。独立 invocation probe 不属于当前业务流程。未改旧 ANI 契约、前端或集群资源。

本轮真实 PostgreSQL create 链路已通过：worker 每步重建并从 PG 恢复，配额 `requests/limits` 快照不会在解码时丢失，GET/LIST 读取 publication 表的最新有效状态；同 generation Watch 通知不会被旧 ack 吞掉。已补当前/上一代 reservation 和坏快照拒绝测试，详情见[PG 生命周期纠错记录](records/2026-09-12-pg-lifecycle-corrections.md)。provider 仍为 fixture，不能据此声称真实配额结算或模型服务可调用。

跨代 PG 生命周期已通过：`create → update → restart → stop → start → stop → delete`；新代发布不会覆盖旧代，配额释放结果已落库后的重试不会再次释放，删除 tombstone 后可正常确认 work。已验证发布/释放结果落库与 operation 完成之间的注入中断恢复；最终全量 Go 测试（含真实 PG）、vet、build 通过，见[跨代生命周期记录](records/2026-09-12-replacement-lifecycle.md)。

配额请求现在保留单容器 `requests/limits`，并从不可变 spec 派生 aggregate demand：Deployment 按 replicas，LWS 按 `replicas × (worker_replicas + leader)`；不复制 Core 配额账本。模型事实现在区分 `model_ready_known` 与 ready 值，不由 Kubernetes runtime observer 推断。真实 authority adapter 和 IAM、Service endpoint 的真实 API/数据面确认、模型加载、调用健康和限流接线仍未实现。恢复测试仅运行随机租户的修复子路径，避免全库 Notify；业务操作的正式进程重启、真实 provider 成功但本地结果未落库、真实 K8s/LWS/引擎/数据面仍为 `not_verified`。

配额聚合证据见[配额聚合记录](records/2026-09-12-quota-aggregate-demand.md)：真实 PG 生命周期测试中，初始 Deployment 为 1 unit，切换到 1 个 LWS group/3 workers 后为 4 units；这里只验证本地需求计算，Core authority 结算仍未接入。

最新开发机验证：`GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1`、`go vet -p 1 ./...` 与 `go build -p 1 ./...` 通过。运行时 apply 后立即 binding 持久化、binding 失败传播、CR control binding、publication intent/observed 分离、旧 generation binding 拒绝、model readiness known fact、LWS worker readiness 和本地 admission 的聚焦测试通过；`sqlc generate` 已从唯一查询源重新生成。隔离 API server 已验证 Deployment/Service/LWS 并发 Conflict，并验证官方 LWS controller 创建 StatefulSet；真实 Pod/GPU、多节点、provider、引擎、数据面及进程级 crash 恢复仍为 `not_verified`，详见[运行时 binding 边界记录](records/2026-09-12-runtime-binding-boundary.md)、[CR control binding 记录](records/2026-09-12-cr-control-binding.md)、[publication/generation fencing 记录](records/2026-09-12-publication-and-generation-fencing.md)、[model readiness 记录](records/2026-09-12-model-readiness-fact.md)和[LWS worker readiness 记录](records/2026-09-12-lws-worker-readiness.md)。
当前最具体的实现前置条件和缺口清单见[remaining gaps 记录](records/2026-09-12-remaining-gaps.md)：Service endpoint 的真实 API/数据面确认、真实 provider、真实 Pod/GPU、多节点 LWS、引擎、数据面和进程级 crash 恢复仍不得标记为已完成。独立 invocation probe 不属于当前业务前置条件。controller-runtime API server/cache/watch 已使用隔离 envtest 通过，证据见[envtest watch 记录](records/2026-09-12-envtest-watch.md)。

本轮复验：隔离 Kubernetes 1.36.2 envtest watch 测试通过；开发机 PostgreSQL 集成包测试通过（含 `applied_generation`、endpoint nullable 列真实落库、GET/LIST 投影和 observation 单事务路径）；带 `INFERENCE_PG_DSN` 的全量 `go test -p 1 ./... -count=1`、`go vet -p 1 ./...` 和 `go build -p 1 ./...` 通过；HTTP invocation probe、显式 endpoint typed Service renderer 以及 Service apply/binding focused tests 通过。`000010`、`000011`、`000012` 已应用到 `recycling-postgres/ani_inference`。本轮证据见[continuation verification 记录](records/2026-09-12-continuation-verification.md)和[发布后健康与 LWS 纠错记录](records/2026-09-12-post-publication-health-and-lws.md)。

Update 当前要求 `update_mask` 同时包含 `resource`、`replicas`、`runtime`，并允许额外的 `engine`/`model_artifact` 字段；PG spec clone 对引擎和制品字段采用非空覆盖、空值保留上一代的规则。

Create/Read use case 已可通过 `ANI_DATABASE_DSN` 接入 PostgreSQL pool；未设置该环境变量时仅保留 layout/健康进程，业务 RPC 会明确返回未配置错误，不会伪造已持久化。GET 只读 PostgreSQL projection，不推进生命周期。

`inference_specs.model_id` 已明确为可选的外部 Model 领域引用；Inference 的必填身份是 `model_version_id`/制品引用，不会伪造 Model UUID 或接管 Model 生命周期。

待评审的关键点和未固定依赖只在[规格第 11 节](../specs/inference-service.md)维护；来源、旧能力清单和实际检查结果见[本次记录](records/2026-09-10-design-baseline.md)。

Service endpoint 的内部契约提案和实施顺序见[ADR 0002](../adr/0002-service-endpoint-contract.md)；内部实现可继续，外部消费接线尚未验证。

本轮新增 endpoint Service 删除观察测试切片：代码覆盖 publication withdrawal 门禁、
旧 resourceVersion 冲突、terminating Service 不等同 absence、finalizer 清除后的
absence 以及同名替换 UID fencing。Kubernetes package 单元测试通过；当前沙箱禁止
envtest API server 回环监听，真实 Service API 证据仍为 `not_verified`，需在开发机
允许监听的环境重跑对应 envtest 命令。

本轮复验：新增 endpoint Service 删除观察测试后，无 PG 环境的全量
`go test -p 1 ./... -count=1`、`go vet -p 1 ./...`、`go build -p 1 ./...` 通过。
带 `ANI_DATABASE_DSN`/`INFERENCE_PG_DSN` 的全量回归被当前沙箱禁止访问
`localhost:5432`（`socket: operation not permitted`），不能替代开发机真实 PG 证据，
因此 PG 状态保持 `not_verified`。

本轮重新执行 `sqlc generate` 通过；无 PG 环境的全仓 test/vet/build 通过。Buf CLI
本机未安装，但已从缓存源码在 `/tmp` 构建临时 CLI，Buf lint 通过。
未修改 Proto、旧 ANI、前端、集群或远程仓库。

补充验证：从本机缓存的 Buf v1.32.2 源码构建 `/tmp/ani-buf` 后执行 `buf lint` 通过；
Proto 契约当前可标记 pass。临时 CLI 不属于仓库产物。

本轮 gRPC/领域定向回归通过：`internal/service`、`internal/biz/inference`、
`internal/biz/work` 测试覆盖租户边界、幂等 hash、generation CAS、ListOperations 分页、
GPU resources normalization、LWS shape、审计 actor 和 durable recovery 顺序。

本轮真实依赖检查：`recycling-postgres` 容器 healthy，容器内 pg_isready 通过，
Inference 表已存在于 public schema；宿主机用用户提供的 `recycling_pass` 连接时
收到 PostgreSQL `28P01`，真实 PG 集成测试保持 `not_verified`。未读取或修改凭据，
未执行迁移/清库。

新增真实 PG 只读 schema 审计：healthy 容器中的 11 张 Inference 业务表均有
`tenant_id`，均未启用 RLS，关键业务关系存在 tenant-scoped 复合外键；schema 约束
证据 pass。宿主机 Go 集成测试仍因实际认证密码与提供值不一致保持 `not_verified`。

新增 sqlc 租户谓词静态审计：全部 `queries/*.sql` 业务查询均显式包含
`tenant_id` 或 tenant-scoped join；唯一跨租户入口为 `ListTenantScopes`，后续仍逐租户
执行。未发现绕过租户范围的 sqlc 查询；真实 PG 负向执行测试仍受认证配置阻塞。

重要复验：将 PostgreSQL 测试编译为静态二进制，在 healthy 的
`recycling-postgres` 容器内通过 Unix socket 运行，全部 `internal/data/postgres`
集成测试通过。覆盖命令/generation、租户隔离分页、quota 资源快照、operation CAS、
跨代 publication/replacement、runtime UID/RV、status projection、work lease/recovery/
repair 和 metrics。宿主机提供的密码仍无法用于 TCP 认证，但不再影响容器内真实 PG
证据；未执行迁移、清库或集群操作。

新增 legacy boundary 审计：`go list -m all` 和 go.mod 未发现 ani-core-platform、旧 ANI
module 或本地 replace；源码中的 reconcile 类型均为本仓库自有领域/官方
controller-runtime 接口，没有旧 PlatformWorkload 或旧 ANI runtime import。

新增 Kubernetes 契约审计：CRD、typed API、controller 和 renderer 统一使用
`ani.kubercloud.com/v1`，未发现旧 `inference.example.io` group；endpoint 稳定命名和
LWS discovery Service 隔离已有 renderer/controller 测试覆盖。

进程边界复核：现有正式进程测试只验证无外部依赖 Kratos `/readyz`、gRPC health、
优雅停机和 listener 关闭，不携带 PG DSN，不能替代 PG-backed business RPC 或
operation crash recovery 证据；该限制已记录，进程级 operation crash recovery 仍为
`not_verified`。

本轮修复 endpoint terminating 观察边界：Service 已进入删除流程但尚未消失时，持续
观察现在返回 `degraded`/`endpoint service is being deleted`，不会当作可调用；删除
观察仍等待真正 absence。新增回归测试按 TDD 先失败后通过；Kubernetes 包、全仓
`go test -p 1`、`go vet -p 1`、`go build -p 1` 通过。

本轮 terminating endpoint 修复后重建并运行真实 PostgreSQL 集成测试：容器 Unix
socket 全部 `internal/data/postgres` 测试通过，`sqlc generate` 通过；数据库 schema
未变化，持久 observation/work lease/generation/CAS/tenant isolation 保持 pass。

新增真实 PG-backed 正式进程证据：静态 Inference binary 在
`recycling-postgres` 容器内通过 Unix socket 连接 PostgreSQL，`/healthz` 与 `/readyz`
均返回 200；发送 SIGINT 后进程退出且 healthz 监听关闭，PostgreSQL 主进程未受影响。
这证明 PG 连接、Kratos readiness 和优雅停机；provider/Kubernetes/operation crash
recovery 仍保持 `not_verified`。

新增 PG-backed gRPC 边界证据：正式进程未注入 tenant context 时，真实 gRPC
`GetInferenceService` 返回 `Unauthenticated: tenant identity is required`，未写数据库；
临时进程随后已停止。gRPC transport 和 tenant trust boundary pass，IAM 正式 middleware
接线仍为 `not_verified`。

gRPC identity wiring review：正式 middleware 只有 recovery/metadata/tracing/logging/
metrics/validation，不把普通 header 当可信 tenant/Actor；业务 RPC 仅接受显式 IAM
adapter 注入的 context。PG-backed 未认证 RPC 已返回 `Unauthenticated`。IAM 正式身份
注入契约仍为 `not_verified`，没有伪造接线。

新增隔离 envtest 真实 API 证据：临时容器使用 Kubernetes 1.36.2 envtest assets 启动
私有 API server/etcd，`TestRuntimeExecutorCRFencingEnvtest/endpoint_deletion_and_observation`
通过；真实验证 endpoint Service 删除门禁、terminating observation、UID/RV fencing 和
最终 absence。未连接现有集群或部署资源。

本轮继续验证：无外部依赖的全仓 `go test -p 1 ./... -count=1`、`go vet -p 1 ./...`
和 `go build -p 1 ./...` 均通过。该结果只证明当前源码和本地 fake/单元路径没有回归；
真实 quota、Model/Storage、IAM、数据面、Pod/GPU、多节点 LWS 以及进程级 operation
重启恢复仍保持 `not_verified`，未把 Network 的 informer 观察证据替代 Inference 的
运行时控制证据。

本轮在开发机隔离 envtest 上运行 Kubernetes 包全量回归通过：使用 Kubernetes
1.36.2 envtest assets 和官方 LWS v0.10.0 CRD，controller-runtime CR/runtime watch、
Deployment/Service/LWS typed SSA 冲突、Service 删除/terminating/absence、UID/RV
fencing、状态投影并发隔离、官方 LWS controller 创建 StatefulSet、Pod worker readiness
以及 Job wake-only 映射均通过。该环境未连接现有集群，也未部署真实 Pod/GPU；真实
调度、多节点 LWS、引擎和进程级 operation 重启恢复继续为 `not_verified`。

本轮在开发机 `recycling-postgres` 容器内用 Unix socket 和当前版本静态测试二进制
执行 `internal/data/postgres` 全量集成测试，全部通过。覆盖真实事务、tenant-scoped
列表、quota resources、operation step CAS、runtime binding UID/RV、publication
generation、status/control identity、Update generation CAS、resource_work lease、
启动恢复和 repair。未迁移、清库或修改旧仓库；外部 quota authority、Model/Storage、
IAM、数据面和进程级正式重启仍为 `not_verified`。

本轮修复正式进程 readiness 门禁：配置 PostgreSQL 但未启用 Kubernetes worker 时，
进程不再因缺少 background server 而初始 ready；否则可能接受已落库但永远无人执行的
operation。新增回归测试覆盖依赖无 worker 和无依赖 layout 两种路径；在开发机正式
进程中验证 `/healthz` 返回 200、`/readyz` 返回 503，随后临时进程已停止。该修复后
全仓 test/vet/build、sqlc generate 和 Buf lint 均通过。

本轮复验 readiness 修复后的当前版本 PostgreSQL 集成测试：静态测试二进制在
`recycling-postgres` 容器 Unix socket 中执行，退出码 0；持久 operation、租户隔离、
generation/CAS、runtime binding、publication、resource_work lease/recovery 和状态
投影保持通过。

本轮完成内部完整性扫描：InferenceServer 的 Create/Get/List/Update/Start/Stop/Restart/
Delete/GetOperation/ListOperations 均有实现；扫描到的 provider/work/controller 门禁
错误均为显式依赖缺失路径，没有隐藏成功或未处理 TODO。核心 service、operation、worker
和 Kubernetes 包定向测试通过；外部 quota、Model/Storage、publication/data-plane、
IAM 和真实调度依赖继续保持 `not_verified`。

2026-09-14 当前集群真实 GPU 验证：在专用 namespace 创建唯一 Pod，requests/limits
均为 `nvidia.com/gpu: "1"`，调度到 `dev-phys-02` 并达到 Running/Ready；容器内
`nvidia-smi -L` 成功发现 `NVIDIA GeForce RTX 4090`。GPU Pod 已删除并确认 namespace
无残留 Pod。该证据证明当前节点设备插件、整卡扩展资源和 GPU 调度可用；Inference
Deployment/LWS 的 GPU 组合、真实引擎、配额结算和 operation 恢复仍需单独验证。

2026-09-14 PG+Kubernetes 正式进程预检：将当前 context 的临时 flattened kubeconfig
与静态 binary 放入 PostgreSQL 测试容器，进程成功启动并连接 PG 与集群 API；`/healthz`
返回 200，`/readyz` 返回 503。503 符合当前未配置真实 admission/model/quota/publication
provider 的 readiness gate，避免无完整 worker 时接受不可执行 operation。进程随后已停止；
未创建 Inference runtime workload。

2026-09-14 当前集群预检与 CPU 运行验证：用户授权使用 context
`kubernetes-admin@kubernetes`。InferenceService CRD 已安装并 Established，专用 namespace
`ani-inference-e2e-20260914` 已创建；LWS v0.10.0 controller 已确认运行。使用集群已有
nginx 镜像创建唯一 CPU Deployment/Service，真实 Pod 在 dev-phys-02 上达到 Ready，
Service EndpointSlice/Endpoints 出现；随后 Deployment、Service 和 Pod 已清理。该切片
证明集群调度和 Service 基础行为，尚未证明 Inference engine、GPU、多节点 LWS、真实
provider 或正式 worker 进程恢复。

2026-09-14 当前集群 CR/LWS 验证：InferenceService CRD 已在
`kubernetes-admin@kubernetes` 安装并 `Established=True`。专用 namespace 中创建的
stopped InferenceService CR 被真实 API 接受；同时创建 1 组、每组 3 个进程的 CPU
LeaderWorkerSet，官方 LWS v0.10.0 controller 成功创建 StatefulSet，3 个 Pod 分别
在 dev-phys-02、kubercloud、dev-phys-03 达到 Running/Ready。测试 CR、LWS、StatefulSet
和 Pod 已删除并确认 namespace 无残留。该证据覆盖真实多节点 LWS controller handoff
和 Pod 调度；GPU、Inference worker 进程、模型加载、真实 provider 和数据面仍为
`not_verified`。
### 2026-09-14：正式外部 Model/Quota 依赖边界（pass/not_verified）

- 已确认：正式路径不使用固定模型、固定配额或 `development profile`；Inference 仅保存外部 Model/Quota 权威返回的引用和结果。
- 已确认：Model、Quota、Publication、IAM 必须通过版本化外部契约接入；provider 未配置时业务 readiness 保持失败。
- `pass`：本地 provider 接口、持久 operation 状态机和 readiness gate 已存在。
- `not_verified`：真实 endpoint、协议、鉴权、幂等、结算回查及模型制品可用性。
- 证据：[2026-09-14-formal-provider-boundary.md](records/2026-09-14-formal-provider-boundary.md)
# 2026-09-14：Model 服务职责边界

- 已确认：Model 是独立服务；它拥有模型目录、版本、制品和默认 engine 启动命令。
- 已确认：Inference 不接管 Model 的物化状态；`model_ready_known`、runtime ready 和 invocation health 是 Inference 自己的运行事实。
- 已确认：Inference 使用 Model 返回的 `engine_type/startup_command/startup_args` 生成最终 `engine_runtime/command_argv`。
- `not_verified`：Model gRPC 契约、真实 endpoint、鉴权和制品协议。
- 证据：[2026-09-14-model-contract-boundary.md](records/2026-09-14-model-contract-boundary.md)
# 2026-09-14：Model 原型契约兼容

- 已只读核对旧 ANI Model Proto：`GetModelVersion`、`GetModelDownloadURL` 及制品/校验字段可复用。
- 已确认新方案采用兼容优先：保留旧方法和字段语义，仅增量增加可选 `engine_type`、`startup_command`、`startup_args`。
- `pass`：当前 Inference 已有 artifact 和 engine/argv 快照字段。
- `not_verified`：新 Model 服务 Proto、endpoint、鉴权和真实联调。
- 证据：[2026-09-14-model-legacy-contract-compatibility.md](records/2026-09-14-model-legacy-contract-compatibility.md)

2026-09-19 新增生产 Publication Kubernetes 适配器：Inference 组合根现在注入
`HTTPRoutePublisher`。publish 会按 tenant/service/generation 生成确定性 HTTPRoute，引用
Inference-owned `-endpoint` Service 和 APISIX Gateway；withdraw 使用 UID/resourceVersion
前置条件删除并等待路由消失；published 只在目标 Gateway parent 同时报告
`Accepted=True`、`ResolvedRefs=True` 后确认。HTTPRoute 现在按 Gateway catch-all 路径匹配；外部返回地址由必填的
`ANI_APISIX_PUBLIC_BASE_URL` 和路径前缀组成，当前 NodePort 可直接调用而不需要 Host
header。多个模型共用同一 Gateway 时仍需不同路径或 hostname 路由。该切片的 fake
client 和全仓源码验证已通过；真实 APISIX controller、DNS/Host 解析、IAM、Model 和
Quota 尚未完成，因此不代表完整 create/stop/restart/update/delete 联调已验收。

Model 物化组合接线已补齐：配置 `ANI_MODEL_GRPC_ADDR` 和 fetcher image 后，Kubernetes
runner 会调用 Model 的 `GetModelVersion`/`GetModelDownloadURL`，创建带校验摘要的 RWX
PVC 和下载 Job，并在 Job 完成后才允许 runtime/publication 继续。当前仅完成代码和 fake
测试；真实 Model gRPC、签名 URL、fetcher image、PVC/StorageClass 和 Quota provider
仍未完成联调。
