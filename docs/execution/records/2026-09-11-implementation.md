# Inference 实施记录（初始切片）

日期：2026-09-11。

## 已落地

- 设计和实施计划：`docs/superpowers/specs/2026-09-11-inference-operator-design.md`、`docs/superpowers/plans/2026-09-11-inference-operator.md`。
- 固定 layout 运行骨架已从只读 pinned checkout 导入到 `cmd/ani-inference-service`、`internal/server`；配置 Proto 已统一到 `api/inference/v1`，当前未运行官方 `scripts/new-service`。
- 独立 `api/inference/v1/inference.proto` 与 `api/inference/v1/conf.proto`，业务和配置 Proto 源文件、生成代码统一位于 `api/inference/v1`，资源字段为 Kubernetes `requests/limits` map，不含 Accelerator 字段。
- `config/crd/bases/ani.kubercloud.com_inferenceservices.yaml`。
- migration、sqlc 配置和集中查询输入，包含 tenant-scoped service/spec/operation/runtime/work/quota reservation/idempotency/audit。
- `sqlc generate` 已成功生成 `internal/data/postgres`；`scripts/migrate-local.sh` 已提供本机数据库创建和 migration replay 命令，凭据只从环境变量读取。
- `internal/data/kubernetes/renderer.go` 已实现确定性 Deployment 渲染及 service ownership 标签；其单测需完整 Kubernetes 依赖后再运行。
- `api/inference/crd/v1` 已加入 typed `InferenceService`/List API 和 DeepCopy，`internal/data/kubernetes/controller.go` 使用该类型完成 controller-runtime Watch/Reconcile：校验 tenant/service ownership labels、调用领域 reconciler、投影 observedGeneration，并用 `RequeueAfter` 作为丢失事件后的唤醒保险。该层不把 CR status 当业务账本。
- `internal/data/postgres/repository.go` 已实现 CreateService 本地事务，原子写入 service/spec/operation/runtime/resource_work/pending quota reservation/idempotency/audit；幂等冲突和同 hash 重放均按 tenant + method + key 处理。
- `inference_specs.model_id` 已改为可选外部 Model 引用；公开入口仍以 `model_version_id` 和制品引用为必填，避免 Inference 伪造或接管 Model 身份。
- `internal/data/kubernetes/renderer.go` 已改为 typed `*appsv1.Deployment`，使用 `corev1.ResourceRequirements` 和 `resource.ParseQuantity`；generation 不进入 immutable selector，ownership labels 使用 `ani.kubercloud.com/*` 并同时校验 tenant/service。
- controller-runtime `AddToScheme` 同时注册 `apps/v1` 与 `core/v1`，避免后续 typed client 使用未注册的 Kubernetes 类型。
- 资源数量校验、stop/delete publication 门禁、generation 防旧结果和 durable worker 核心及单测。

## 验证证据

- 通过：在隔离临时 module 中运行 `go test ./...`，覆盖 `internal/biz/resources`、`state`、`work`、`admission`。
- 未验证：Kubernetes、GPU、Quota 远程协议、publication、IAM、Model 和数据面。Proto 已用缓存的 protoc-gen-go v1.36.11 生成。
- 通过：安装固定 Kratos CLI `v3.0.0-20260626125723-668db92c2c00` 与 Buf `1.60.0` 到临时目录，在 `/tmp/ani-inference-service` 运行 layout `scripts/new-service`，生成器完成 module/tree 检查；该临时输出未复制回当前仓库。
- 通过：Go 1.26.8 下执行 `GOCACHE=/tmp/ani-go-cache GOPROXY=off GOSUMDB=off go test -timeout 30s ./...` 和 `go build ./...`；仅进程监听测试因沙箱禁止 bind 被测试内 skip。
- 通过：在 `recycling-postgres` 中创建独立 `ani_inference` 数据库并执行 migration；确认 8 张表、`relrowsecurity=false`，跨租户复合 FK 负向测试通过。尚未执行 operation 并发、真实 K8s、Quota 或数据面测试。
- 通过：使用 `INFERENCE_PG_DSN=postgres://recycling_user:***@127.0.0.1:5432/ani_inference` 运行 `TestCreateServiceTransactionIntegration`，验证真实 PostgreSQL 首次事务写入、pending quota reservation 和同 key/hash 幂等重放。
- 通过：在临时 PostgreSQL 数据库 `ani_inference_verify_20260911` 全新执行 migration，得到 8 张表且 `inference_specs` 无 RLS；验证后已删除该临时数据库。
- 通过：`go test -mod=readonly ./...`、`go vet ./...`、`go build ./...`；监听测试在受限沙箱中按环境跳过。
- 通过：typed Deployment renderer 和 ownership/selector/resource 单测；一次完整测试执行中 `cmd` 进程信号测试因受限沙箱子进程未退出而在 60 秒超时，需在非受限环境复验该进程测试。
- 限制：缓存的第三方 `controller-gen v0.11.1` 与 Go 1.26/Kubernetes 0.36 组合执行 object 生成时触发 loader panic；没有把该不匹配版本加入服务依赖，DeepCopy 文件按其生成格式保留，待升级匹配版本后重新生成验证。
- 通过：Create gRPC 已完成认证上下文中的 tenant 提取、资源量规范化、确定性请求哈希和异步 operation 响应；`internal/data/postgres` 的 CreateUseCase adapter 将其映射到现有原子事务。当前 `buildApp` 仅支持注入该 use case，实际 PG pool/DSN composition 接线仍未完成，不能据此宣称正式进程已具备持久写入能力。
- 通过：资源量解析已改用 Kubernetes 官方 `resource.ParseQuantity` 与资源名校验，不再维护自定义数量单位解析器；Deployment 渲染继续使用 typed `corev1.ResourceRequirements`。
- 通过：`cmd/ani-inference-service` 在设置 `ANI_DATABASE_DSN` 时创建并 Ping `pgxpool`，注入 PostgreSQL CreateUseCase，并在进程退出时关闭连接池；未设置 DSN 时保留显式未配置行为，避免把无持久化进程误报为完整服务。
- 通过：新增 PostgreSQL 只读 adapter 和 `GetInferenceService`/`GetOperation` gRPC 链路；查询显式带 tenant_id，`pgx.ErrNoRows` 映射为 `NOT_FOUND`，读取不触发 worker、观察或配额副作用。
- 通过：开发机联网下载并加入官方 `sigs.k8s.io/lws v0.10.0`，LWS renderer 使用 `sigs.k8s.io/lws/api/leaderworkerset/v1` typed API；当前真实集群 CRD/controller 调度与 group/worker 观察仍为 `not_verified`。
- `000002_inference_lws.sql` 增加 runtime_mode/worker_replicas、LWS readiness counters、LWS UID 和 active operation 唯一索引；已在开发机 `recycling-postgres/ani_inference` replay，并确认字段、索引存在且 RLS 仍关闭。

## 环境限制

当前环境已升级为 Go 1.26.8，满足 layout 的 Go 1.26.7 下限。依赖缓存已足够完成本地构建；网络 proxy 的 DNS 仍被执行沙箱拒绝，固定 CLI/Buf 流程尚未验证。受限 shell 看不到宿主机 5432，但通过已授权的 `docker exec` 完成了本机 PostgreSQL migration replay。

本记录没有执行集群、远程仓库、提交或推送操作。
