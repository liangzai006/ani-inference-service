# 2026-09-12 remaining implementation gaps

## 2026-09-18 scope correction

独立 invocation probe、`HTTPProbe` 和 `verify_invocation` 不再属于 Inference
业务 operation 的必要流程；Kubernetes 容器的 startup/readiness probe 负责运行状态。
本记录中关于 invocation probe 的内容保留为历史设计证据，不再作为当前上线前置条件。

## 已完成且有源码/本地 PG 证据

- gRPC 受理、幂等、generation CAS、持久 operation 和 resource work。
- PostgreSQL startup recovery、租约/CAS、operation step/audit 原子写入和
  terminal observation 扫描。
- controller-runtime CR/runtime watch 唤醒、typed Deployment/LWS/Pod 观察、
  CR control binding、UID/resourceVersion/generation fencing。
- CRD `status` 自触发已通过 `GenerationChangedPredicate` 排除；显式 endpoint
  也会投影到 typed CR spec。
- gRPC/PG/CRD 已贯通 `served_model_name`；controller-runtime 已注册 Job
  runtime watch，事件仍只负责唤醒持久 work。
- Kubernetes resources requests/limits、GPU 扩展资源、LWS worker readiness、
  model readiness known fact、publication 撤回确认状态机和显式
  `InvocationProbe` readiness gate。

## 仍未完成或只能标记 not_verified

1. **Service/endpoint 契约**：gRPC、CRD、PG nullable columns/sqlc、Read/RuntimeSource
   和 typed Service apply/binding 已接通；只接受显式 containerPort、Service port、
   targetPort、协议和 generation selector。仍需补 Service 删除/观察的真实 API 证据、
   TLS、稳定名称及数据面确认合同。Service 生效也不能直接等同 publication。
2. **真实 provider**：Core quota 的 Reserve/Confirm/Release、Model/Storage
   物化、publication/data-plane 和 IAM 契约尚未提供，不能在 Inference 内复制
   临时账本或伪造 provider。
3. **真实调用健康**：已有 HTTP adapter、发布后 `verify_invocation` 持久步骤和
   terminal observation 的持续探测；显式 unknown 与跨代隔离已通过真实 PG
   测试。真实鉴权/HTTP/gRPC 调用协议、engine health 和发布地址仍未验证。
4. **真实 Kubernetes**：API server/cache/watch、Deployment/Service/LWS typed
   SSA 的 GET→apply Conflict 已用隔离 envtest 验证；官方 LWS controller 创建
   StatefulSet 的隔离 envtest 也已通过。LWS controller 调度到真实 Pod、GPU
   调度、LWS 多节点和真实集群 CRD 安装仍需隔离测试集群。
5. **进程级恢复证据**：已有 PG recovery 代码和集成测试，但仍需执行真实
   进程在 operation/lease 中断后的重启场景，验证旧 lease/旧 generation 结果
   不能覆盖新状态。
6. **applied generation 对外/CRD 语义**：`000010` 已增加
   `inference_services.applied_generation`，observation CAS 在同一事务内推进，
   gRPC GET/LIST 和 CRD status projector 只读投影该值。当前已接入 PostgreSQL
   单语句只读 source、APIReader + 带乐观锁的 status-only patch 和 Controller；
   隔离 envtest 已验证真实 status 子资源、冲突重读及调用期间 UID/租户变化拒绝，
   实际 PG 已验证 stale-after 和旧 publication 的 phase/effective 分离。
   首次投影已补持久 control binding 的 namespace/name/UID 核验，真实 PG 与
   隔离 API 重建场景通过；CRD `ObservedGeneration` 与 API 命名的消费者合同尚未冻结。
   详见[绑定身份记录](2026-09-12-status-control-identity.md)。
7. **领域指标**：请求、进程和 readiness 指标已有；worker claim/空扫描/完成/重试/
   失败计数、固定 `error_class`、PG backlog/最老工作年龄和 observation stale
   聚合 gauge 已接入；模型物化时延和 bounded 依赖错误分类也已接入。真实
   Model/Storage provider 的时延、错误 taxonomy 和端到端数据仍为 `not_verified`，
   见[模型物化指标记录](2026-09-12-model-materialization-metrics.md)。
8. **审计上下文**：已复用 `inference_audit_events` 的受理事件作为持久调用上下文，
   `OperationStore` 按 tenant+operation 读取 Actor/RequestID，Runner 的原子 step/retry
   事件写入 Actor、RequestID、before/after phase/step 状态；领域单测与真实 PG
   `OperationStore` 测试通过。IAM middleware 如何提供可信 Actor、正式审计字段分类
   和跨服务汇总仍为 `not_verified`，Inference 不复制 IAM 权威。
9. **操作列表**：已补 tenant-scoped `ListOperations` gRPC、PG keyset 查询和可选
   resource 过滤；跨租户/分页集成验证已覆盖本地 PG。取消操作、服务端排序/过滤扩展
   仍不在本期范围。
10. **Job 运行事实**：当前仅注册 batch/v1 Job 的唤醒 watch；Inference runtime
    apply/observation/binding 仍只支持 Deployment/LWS/Service，Job 若由 Model/Storage
    provider 创建必须通过明确外部事实协议接入，不能把该 watch 误标为 Job 生命周期已完成。
11. **限流执行**：当前仅保留资源/配额需求和控制面状态，调用限流仍属于推理数据面
    /Envoy；需要固定发布配置与授权结果的消费契约，不能在控制面复制一套限流器。
12. **CR spec/runtime fencing**：`applyCR` 已补持久 control UID 和当前 API RV
    的 SSA fencing，`DeleteCR` 已改用当前 API UID/RV precondition；fake 与隔离
    envtest 通过；Deployment/Service/LWS 的真实 GET→apply 并发 RV 回归也已通过。
    官方 LWS controller 的 StatefulSet handoff 已通过隔离 envtest；仍需验证
    真实 Pod/GPU 行为和进程级 runtime 恢复。

## 实施顺序

endpoint 的内部字段、typed Service、同代 selector 和 UID/RV binding 已实现；官方
LWS controller 的 StatefulSet handoff 已由隔离 envtest 覆盖；下一步用 envtest/隔离
集群补 Service API、真实 Pod/GPU 与 LWS 行为验证，再接入版本化外部 quota、Model、
publication、IAM 和 data-plane 合同。任何一项没有真实依赖证据，都保持 `not_verified`。

本轮配额补充：reservation 的 `last_error_code` 已由 OperationStore 按租约写回，Confirm/Release 失败分别保留稳定诊断码，成功时清除。worker 已接入 `quota.Port.Get`：恢复时只有 authority 明确返回 `ErrReservationNotFound` 才允许重新 Reserve，其他查询错误继续重试。外部 authority 的错误分类、幂等键和按 reservation 查询协议仍未确定。

配额恢复查询补充：worker 现在区分 authority 明确 `ErrReservationNotFound` 与其他查询错误；只有前者允许重新 Reserve，其他错误进入重试。真实 authority 必须实现该错误分类和 reservation identity 查询合同。

配额确认恢复修正：authority 查询返回 `confirmed` 时，worker 现在先将 reservation 和当前 lease/fence 持久回 PG，再推进 `apply_cr`，避免下一步重新读取到旧的 `pending` 状态。

状态投影补充：新增只读 `StatusProjectionSource` 与 controller-runtime
`StatusProjector`。它通过 APIReader 读取最新 CR，按标签从 PostgreSQL 读取
`applied_generation`、runtime、publication、invocation 和当前 operation，再用
`Client.Status().Patch` 写入 CR status；status 写入不推进 operation、work 或 PG
状态。CR `status.observedGeneration` 明确定义为 PG `applied_generation`，不使用
Kubernetes `metadata.generation`。真实 API server 的 status 子资源和 RV 冲突
已用隔离 envtest 验证；首次投影的持久绑定核验现已实现并通过真实 PG 回归，
见[状态并发记录](2026-09-12-status-concurrency.md)和
[绑定身份记录](2026-09-12-status-control-identity.md)。

## 2026-09-12 endpoint deletion API slice

新增 `endpoint_envtest_test.go`，在已有隔离 envtest harness 中覆盖 Inference-owned
Service：必须先有 publication withdrawal 才允许删除；持久 binding 的旧
resourceVersion 被 API 拒绝时不能删除；DELETE 进入 terminating 但 finalizer 尚未
消失时 `ObserveAbsence` 仍返回 degraded；finalizer 清除后才确认 absence；旧 UID
不能删除同名替换 Service。该测试代码和 Kubernetes package 单元编译/测试通过。
当前执行环境禁止 envtest control plane 绑定 `127.0.0.1:0`（`operation not
permitted`），所以真实 API 运行证据仍为 `not_verified`，需要在允许本机回环监听的
开发机环境重跑：
`KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.36.2-linux-amd64 go test -p 1 ./internal/data/kubernetes -run 'TestRuntimeExecutorCRFencingEnvtest/endpoint_deletion' -count=1 -v`。

## 2026-09-12 continuation verification

新增 endpoint 测试后的 `go test -p 1 ./... -count=1`（无 PG 环境变量）通过，
`go vet -p 1 ./...` 与 `go build -p 1 ./...` 通过。带本机 PostgreSQL DSN 的
回归未能建立 `localhost:5432` 连接，当前执行沙箱返回 `socket: operation not
permitted`；该结果不能证明 PostgreSQL 失败，真实 PG 证据保持 `not_verified`，
需要在允许本机回环连接的开发机执行同一命令。

## 2026-09-12 generator verification

本轮重新执行 `sqlc generate`，退出码 0。全仓无 PG 环境变量的 Go 测试、vet 和
build 仍通过。Buf CLI 未安装；尝试使用本地 module cache 的 `go run` 时被当前
沙箱拒绝写入 Go build cache（read-only filesystem），因此 Buf lint 本轮保持
`not_verified`，没有修改 Proto 或生成文件。

## 2026-09-12 Buf lint verification

从本机缓存的 Buf v1.32.2 module source 在可写 `/tmp` build cache 构建临时 CLI，
执行 `/tmp/ani-buf lint` 通过。此前的 Buf `not_verified` 仅是命令安装/缓存权限
问题，现已获得当前 Proto 契约 lint 证据；未修改远程仓库或旧 ANI。

## 2026-09-12 gRPC contract regression

`internal/service`、`internal/biz/inference` 和 `internal/biz/work` 定向测试通过，
覆盖 tenant context、幂等 request hash、generation command、ListOperations 分页、
资源/GPU normalization、LWS runtime shape、审计 actor 传播和 durable worker 恢复调用顺序。

## 2026-09-12 PostgreSQL container verification

通过只读 Docker 检查确认 `recycling-postgres` healthy，容器内
`pg_isready -U recycling_user -d ani_inference` 返回 accepting connections；当前
`public` schema 存在 11 张 Inference 表（含 services/specs/operations/publications/
quota/resource_work/runtime/bindings/audit）。从宿主机使用用户提供的
`recycling_pass` 运行 PG 集成测试时，数据库返回 PostgreSQL `28P01`
(password authentication failed)，因此测试结果仍为 `not_verified`。未读取、修改或
重置容器凭据，也未执行迁移、清库或数据写入。

## 2026-09-12 PostgreSQL schema read-only audit

在 healthy 的 `recycling-postgres` 容器内通过 `psql` 只读审计当前
`ani_inference` 数据库：11 张 `inference_*` 业务表都包含显式 `tenant_id`；这些
表的 `relrowsecurity` 均为 false；operation、service、spec、publication、quota、
work、runtime、binding、observation 和 audit 之间存在 tenant-scoped 复合外键。
这确认了“无 RLS + 显式租户隔离 + 数据库完整性约束”的实际 schema 证据。宿主机 Go
集成测试仍因连接密码不匹配而保持 `not_verified`。

## 2026-09-12 sqlc tenant predicate audit

对 `queries/*.sql` 的全部 sqlc query block 做静态审计：所有访问 Inference 业务表的
查询都包含 `tenant_id` 字段或 tenant-scoped join/predicate；唯一跨租户入口是
`ListTenantScopes`，只返回租户范围，后续 worker 仍逐租户执行。脚本未发现缺少
`tenant_id` 的业务 query。该结果是源码审计证据，未替代真实 PG 负向执行测试。

## 2026-09-12 real PostgreSQL integration pass

为避免宿主机认证密码不匹配，将 `internal/data/postgres` 测试编译为静态 Go
测试二进制，临时复制到 healthy 的 `recycling-postgres` 容器，并通过
`PGHOST=/var/run/postgresql`、容器内 `recycling_user` Unix socket 执行。全部
PostgreSQL 包测试通过：命令幂等/generation、观察 invocation tenant fence、Create
事务与 quota resources、operation step CAS、读列表 keyset/tenant isolation、跨代
replacement lifecycle、runtime binding UID/RV、publication generation、status
projection/control identity、Update generation CAS、work lease/notification/recovery/
repair/metrics 均 pass。未执行迁移、清库或集群操作。

## 2026-09-12 legacy dependency boundary audit

`go list -m all` 未发现 ani-core-platform、旧 ANI module 或本地 `replace`；源码中
出现的 `internal/biz/reconcile.Reconciler` 是本仓库自己的 lease/generation-fenced
领域适配器，controller-runtime 的 `Reconciler` 仅为官方接口命名。未发现旧
PlatformWorkload、旧 ANI runtime import 或旧生成契约依赖。

## 2026-09-12 Kubernetes contract audit

CRD、typed API 和 runtime tests 统一使用 `ani.kubercloud.com/v1`；仓库业务源码和
配置未发现旧 `inference.example.io` group。Endpoint Service 使用 `<runtime>-endpoint`
稳定名称，LWS discovery Service 不被接管；现有 renderer/controller tests 覆盖
Deployment、Service、LWS GVK 及 endpoint selector 规则。

## 2026-09-12 process readiness boundary review

正式进程测试 `TestMainProcessHandlesSignalAndClosesListeners` 只启动无外部依赖的
Kratos 进程并验证 `/readyz`、gRPC health、优雅停机和 listener 关闭；它不携带
`ANI_DATABASE_DSN`，因此不能证明 PG-backed business RPC 或 operation worker 已在
正式进程中恢复。该限制与 readiness composition 文档一致，真实 PG worker 证据由
容器内 PostgreSQL 集成测试提供，进程级 operation crash recovery 继续保持
`not_verified`。

## 2026-09-12 terminating endpoint observation fix

新增回归测试 `TestEndpointTerminatingIsDegradedBeforeAbsence`：带 finalizer、已进入
删除流程的 endpoint Service 必须让持续观察返回 `degraded`，不能被当作可调用端点，
同时删除观察仍不把它当作 absence。实现增加 `RuntimeObject.Terminating` 事实并在
endpoint observation 中设置明确原因。测试先按 TDD 复现失败，再以最小改动修复；
Kubernetes 包、全仓 Go test、vet、build 均通过。

## 2026-09-12 terminating endpoint PG regression

本轮 `RuntimeObject.Terminating` 变更后，重新编译静态 PostgreSQL 测试二进制并在
`recycling-postgres` 容器 Unix socket 中执行全部 `internal/data/postgres` 测试，退出
码 0；`sqlc generate` 也通过。该字段不改变数据库 schema，真实持久 observation、
work lease、generation/CAS 和 tenant isolation 回归保持通过。

## 2026-09-12 PG-backed process lifecycle pass

构建静态正式 Inference binary，在 `recycling-postgres` 容器内以 Unix socket
`ANI_DATABASE_DSN` 启动（未启用 Kubernetes）。进程实际建立 PG 连接并打开 Kratos
admin/gRPC 生命周期；容器内 HTTP probe 观察到 `/healthz` 和 `/readyz` 均为 `200`。
随后向该临时进程发送 SIGINT，进程退出，healthz 端口返回 connection refused；
PostgreSQL 主进程未停止。该证据证明正式进程的 PG 连接、readiness 和优雅停机，仍
不替代 provider/Kubernetes 或业务 operation crash recovery。

## 2026-09-12 PG-backed gRPC auth boundary pass

在同一 PG-backed 正式进程中使用临时 gRPC client 调用只读
`GetInferenceService`，不注入可信 tenant context；服务返回 gRPC
`Unauthenticated: tenant identity is required`。该调用未写数据库。随后停止临时进程
并确认退出。证明正式 gRPC transport 和 tenant trust boundary 生效；IAM middleware
提供可信 Actor/tenant 的正式接线仍为 `not_verified`。

## 2026-09-12 gRPC identity wiring review

正式 gRPC middleware 当前包含 recovery、Kratos metadata、tracing、logging、metrics
和 validation；没有把普通 metadata/header 自动当作可信 tenant 或 Actor。业务服务只
接受由未来 IAM adapter 显式注入的 `WithTenantID`/`WithActor` context。PG-backed
未认证 RPC 已实测返回 `Unauthenticated`。因此安全边界正确，但 IAM 可信身份注入
契约和正式 middleware 接线保持 `not_verified`，不会用 Header 伪造接入。

## 2026-09-12 isolated envtest endpoint pass

在临时 Go 1.25 容器中自动获取 go.mod 所需 Go toolchain，挂载本机 Kubernetes
1.36.2 envtest assets，启动私有 API server/etcd（network 隔离）。
`TestRuntimeExecutorCRFencingEnvtest/endpoint_deletion_and_observation` 通过，
真实 API server 验证了 endpoint Service 的删除门禁、terminating observation、UID/
resourceVersion fencing 和最终 absence。未连接现有集群，未部署集群资源。
