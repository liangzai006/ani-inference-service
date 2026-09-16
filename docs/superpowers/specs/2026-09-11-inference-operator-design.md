# Inference Operator 与持久控制面设计

状态：已确认设计，实施前版本。日期：2026-09-11。

## 目标与边界

Inference 是独立 Go 服务，基于固定 ani-kratos-layout commit `fd18422211c741dd5242d2992c3d307d522aa28c` 生成并独立维护。它拥有推理服务自身的 desired spec、operation、Kubernetes runtime、模型加载观察、publication、调用健康、配额 reservation 记录、审计和恢复工作。旧 ANI、Core PlatformWorkload、旧 REST/OpenAPI/Proto、旧 Observer/Reconciler 不进入新服务。

Inference 直接访问 Kubernetes API 并写自己的 PostgreSQL；不通过 Core Service 执行 Kubernetes 操作。Model、IAM、Quota、Storage、Network、Cluster/Inventory 的全局权威不复制。配额策略和总账仍由 Core 治理，通过幂等 Reserve/Confirm/Release 协议接入；Inference 只保存 reservation 引用和本地状态。

## 权威划分

- PostgreSQL：业务命令、desired state、generation、operation、quota reservation、审计、runtime observation 和 durable work 的权威。
- `InferenceService` CRD：Kubernetes 侧的期望投影和 Operator 入口；status 不是 operation、配额或审计账本。
- Deployment/Service/Pod/Job：运行事实；只由 Inference 管理自己创建且 UID 匹配的对象。
- Operator：通过 controller-runtime 接收事件并执行 Reconcile；业务数据库访问走 biz/use-case/repository/sqlc。
- Kubernetes Watch、Channel 和 RequeueAfter：只负责唤醒；启动扫描、due-scan 和 durable work 负责恢复。

## gRPC 入口

创建和更新使用独立 `inference.v1` 契约。资源配置直接遵循 Kubernetes ResourceRequirements：

```protobuf
message ResourceSpec {
  map<string, string> requests;
  map<string, string> limits;
}
```

示例：`requests.cpu=2`、`requests.memory=8Gi`、`requests.nvidia.com/gpu=1`；limits 使用同样的扩展资源键。服务端解析 Kubernetes Quantity，校验 requests 不超过 limits。Kubernetes 允许扩展资源只出现在 limits，此时有效 request 等于 limit；若 requests 和 limits 都出现，则两者必须相等。未传 GPU 不注入 GPU，也不自动追加 `--device=cpu`。首期 `replicas` 只允许 1。

Create/Update/Start/Stop/Restart/Delete 都是异步 operation，返回 resource projection 与 operation_id。`request_id` 按 tenant + RPC method 幂等；Update 使用 expected_generation CAS。客户端不能伪造 tenant_id、actor、配额状态或审计主体。

## 数据模型

首期保留以下表：

- `inference_services`：稳定身份、desired_state、desired_generation、当前 operation、墓碑。
- `inference_specs`：每个 generation 的模型、镜像、argv、ResourceSpec 和副本历史。
- `inference_operations`：命令类型、phase、step、target_generation、重试、lease、错误和结果。
- `inference_runtime`：CR/Deployment/Service UID、观察到的 runtime generation、runtime phase、model ready、publication phase、invocation health 和观测时间；service-level `applied_generation` 归 `inference_services` 持有并由 observation CAS 推进。
- `inference_resource_work`：dirty/ack 版本、next run、lease 和重试；保证无用户查询、Watch 丢失和进程重启仍能恢复。
- `inference_quota_reservations`：Core Quota reservation_id、请求资源快照、operation/generation 和本地状态。
- `inference_audit_events`：追加写入的不可变业务历史。若后续需要可靠消息投递，再单独增加 outbox 表。

所有租户业务表带 tenant_id；主键、唯一性、查询、更新、join 和 FK 均限定 tenant_id；不使用 RLS。禁止在业务代码散写 SQL，统一使用 sqlc 生成访问代码。

## 请求到工作的时序

1. gRPC 验证身份、tenant、模型引用、引擎和 ResourceSpec，计算请求 hash 并检查幂等。
2. PostgreSQL 本地事务写 service、spec、pending operation、resource_work 和审计事件。
3. 调用 Core Quota `ReserveQuota`，以 operation_id + generation 幂等；成功后保存 reservation_id/state=reserved，超时进入可重试步骤。
4. 事务提交后以确定名称 Server-Side Apply `InferenceService` CR。Apply 失败不回滚数据库，operation 保持 pending/retryable；启动扫描会恢复。
5. Operator Watch CR/Deployment/Service/Pod 并将事件映射到稳定 service key；同时由 PG due-scan 和启动扫描投递工作。
6. Reconcile 领取 PG work lease，重新读取当前 generation/spec/operation；不使用排队时旧 payload，不在 K8s 调用期间持有 PG 长事务。
7. Operator 校验 CR UID、Kubernetes UID、generation 和适用 resourceVersion，渲染并 Apply Deployment/Service；超时先查询确认，不重复创建。
8. 观察调度、Pod Ready、模型物化/加载、协议探测和有凭证调用健康，分别写 runtime observation。仅目标 generation 且 lease_token 匹配时允许 CAS 写回。
9. runtime ready 后调用 publication provider；确认生效并通过调用探测后才将 operation 标记 succeeded。operation 成功后仍周期观察退化。
10. Stop/Restart/Delete 先撤销 publication、确认停止导流并排空连接，再停止或删除 runtime；确认资源消失后释放 quota reservation，追加审计并完成 operation。

## 并发与恢复

同一 service 串行、不同 service 有界并发。lease 不是 fencing；旧实例必须在每次写回前校验 lease_owner、lease_token、generation。Kubernetes 变更使用 UID/resourceVersion 前置条件；UID 不匹配或缓存/API 不确定进入 unknown/stale，不自动收养或删除同名对象。旧 generation 的延迟观察不得覆盖新状态。

GET/LIST 纯读 PostgreSQL projection，不推进 operation、不触发观察、不结算配额。操作完成、runtime ready、publication 生效和 invocation health 分开建模并带 observed_at/stale_after。

## 配额与审计

Inference 不复制全局配额账本。资源请求按 `requests × replicas` 形成 reservation；Create/Start 先 Reserve，runtime 应用后 Confirm，Stop/Delete 在撤发布、排空和 runtime 消失确认后 Release。远程调用必须可重试、可查询、可补偿；释放失败保留 `release_pending`。

审计事件在 Inference 本地状态事务中追加写入，至少覆盖 InferenceCreated、QuotaReserved、CRApplied、RuntimeApplied、RuntimeReady、PublicationEnabled、PublicationWithdrawConfirmed、RuntimeDeleted、QuotaReleased 和 OperationFailed。事件带 tenant_id、service_id、operation_id、generation、request_id/actor 关联，不写 Secret 或签名 URL。

## 验收范围

分布式 runtime 采用 Kubernetes LeaderWorkerSet（LWS）。`replicas` 表示 worker group 数量，`worker_replicas` 表示每个 group 的 worker 数量；首个 LWS 切片要求 `worker_replicas >= 2`。LWS group/worker 就绪必须分别观察并持久化，未满足目标 generation 的完整就绪、模型加载和调用健康门槛时不能发布。LWS Go API 依赖、集群 CRD 安装和真实调度验证单独记录为 `not_verified`；LWS 不可用时必须报错，不能静默降级成 Deployment。

首期验证单模型、单 Deployment、单 Service、单副本；覆盖无 accelerator、带 `nvidia.com/gpu`、自定义 argv、幂等、generation CAS、CR Apply 失败恢复、Watch 丢失、Operator 重启、Pod 删除、模型不健康、publication 撤销失败、quota release 重试和跨租户负向测试。真实 PostgreSQL、固定 layout 正式进程和隔离 Kubernetes 证据分别记录 pass/fail/not_verified；未接入真实 IAM/Model/Quota/Data Plane 时不得宣称产品链路完成。
