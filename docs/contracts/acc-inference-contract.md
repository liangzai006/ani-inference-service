# ACC / Inference 合同基线

日期：2026-10-18（Asia/Shanghai）

本文件记录本次迁移前的接口基线。读取的版本是：

| 组件 | 版本/提交 |
| --- | --- |
| ACC | `4f084c9f5f2560c9ed00f59db2284c3f991db0a4` |
| Governance | `90b1ecc8cfa31b96ac43a8e4ff3aa18d41f03812`（`main`） |
| Resource | `f7459f7`（`main`） |
| Model | `430d35aa9a18ef058ac0744cbb51e2ffe1bf47ce`（`master`） |
| Inference | 当前工作树 `codex/acc-contract-20261018`；模块中的 ACC 版本为 `v0.0.0-20261016163330-c1242866d987` |

## ACC main 当前合同

ACC main 只有 `accelerator/v1` 和 `accelerator/command/v1` 两组 proto。旧的
`accelerator/integration/v1` 在 ACC main 不存在。

### 解析请求和响应

包：`accelerator.v1`

| 消息 | 字段（名称、类型、编号） | 用途 |
| --- | --- | --- |
| `ResolveGpuRequestRequest` | `context: TenantContext = 1`; `gpu: GpuRequest = 2` | 不占额、不锁设备的规格解析请求 |
| `ResolvedGpuRequest` | `execution: GpuExecution = 1`; `quota_items: repeated GpuQuotaItem = 2`; `selection: GpuDisplay = 3` | 返回执行配置、配额项目和展示用规格 |
| `GpuExecution` | `scheduler_name: string = 1`; `queue_name: string = 2`; `runtime_class_name: string = 3`; `node_selector: map<string,string> = 4`; `pod_annotations: map<string,string> = 5`; `resources: map<string,string> = 6`; `execution_digest: string = 7` | Inference 创建工作负载时使用的执行配置 |
| `GpuDisplay` | `profile_id: string = 1`; `profile_version: uint64 = 2`; `profile_name: string = 3`; `model_key: string = 4`; `mode: SupplyMode = 5` | 对外展示的 GPU 规格 |
| `GpuQuotaItem` | `quota_code: string = 1`; `units: int64 = 2` | 解析出的配额项目 |

`AcceleratorCatalogService.ResolveGpuRequest(ResolveGpuRequestRequest)` 是当前
ACC 的解析 RPC。它是事务外解析，不代表锁定、占用或创建已完成。

### 使用投影和释放观察

| 消息 | 字段（名称、类型、编号） | 用途 |
| --- | --- | --- |
| `GpuUsageRef` | `tenant_id: string = 1`; `owner_service: string = 2`; `resource_id: string = 3`; `create_operation_id: string = 4` | 稳定的租户、owner 和资源关联键 |
| `GpuUsageProjection` | `ref: GpuUsageRef = 1`; `revision: uint64 = 2`; `state: UsageState = 3`; `end_reason: UsageEndReason = 5`; `source_fact_ref: string = 6`; `source_operation_created_at: string = 7`; `payload_digest: string = 8`; `profile_id: string = 10`; `profile_version: uint64 = 11`; `group_id: string = 12`; `baseline_id: string = 13`; `cluster_id: string = 14`; `container_name: string = 15`; `execution_digest: string = 16` | Gov 同步声明/结束投影；字段 4 及 `plan` 已保留 |
| `ObserveReleaseRequest` | `request_id: string = 1`; `ref: GpuUsageRef = 2`; `delete_operation_id: string = 3`; `pods: repeated PodRef = 4`; `cluster_id: string = 6`; `sealed_at: string = 7` | owner 在删除对象并封闭创建后请求 ACC 检查释放事实 |
| `ReleaseObservation` | `evidence_id: string = 1`; `ref: GpuUsageRef = 2`; `finding: ReleaseFinding = 3`; `active_pod_uids: repeated string = 4`; `uncertain_node_uids: repeated string = 5`; `observation: Observation = 6`; `evidence_digest: string = 7` | 释放检查证据；`ALLOCATION_RELEASED` 仍不等于退款完成 |

`AcceleratorUsageService` 的 RPC 为：

- `SyncGpuUsage(SyncGpuUsageRequest) -> SyncGpuUsageResponse`
- `GetGpuUsage(GetGpuUsageRequest) -> GpuUsageProjection`
- `ListGpuUsages(ListGpuUsagesRequest) -> ListGpuUsagesResponse`
- `ListBindings(ListBindingsRequest) -> ListBindingsResponse`
- `ObserveRelease(ObserveReleaseRequest) -> ReleaseObservation`

ACC main 的 `ObserveRelease` 明确要求 `cluster_id` 和 RFC3339Nano 的 `sealed_at`。
Inference 删除退额不调用 `ObserveRelease`：运行时由 Inference 删除并由 Inference
观察到不存在（absence seal + Pod UID 范围），即为释放证据，随后直接向 Gov
报告 `ReportResourceReleased`。上表消息仅作为 ACC 侧现有合同记录。
`AcceleratorUsageService` 没有 `RegisterConsumer`、`MarkClosing` 或 `Finalize`。

### 持久命令确认消息

包：`accelerator.command.v1`

| 消息 | 字段 |
| --- | --- |
| `DurableOwnerAck` | `operation_id: string = 1`; `resource_id: string = 2`; `accepted: bool = 3` |
| `GpuDeleteAcceptance` | `delete_operation_id: string = 1`; `resource_id: string = 2`; `result: string = 3`; `dispatch_operation_id: string = 4` |

## 当前 Inference 与 ACC 的实际依赖

Inference 当前 proto `api/inference/v1/inference.proto` 仍直接导入：

```proto
import "accelerator/integration/v1/owner_gpu_attachment.proto";
```

并在 `CreateInferenceServiceRequest`、`DeleteInferenceServiceRequest` 和
`OperationResponse` 中使用该旧包的 owner attachment、`DurableOwnerAck` 等类型。
当前 Go 源码还引用：

- `accelerator.integration.v1.GpuOwnerCreateAttachment`
- `accelerator.integration.v1.GpuOwnerDeleteAttachment`
- `accelerator.v1.ResolvedGpuPlan`
- `accelerator.integration.v1.GpuChargeRef`
- `accelerator.v1.OriginalQuotaCharge`

因此当前 Inference 仍绑定 ACC 旧模块
`v0.0.0-20261016163330-c1242866d987`，尚未消费 ACC main 的
`ResolvedGpuRequest` / `GpuExecution` 合同。

## 将被退休的 Inference 字段

以下字段属于旧 owner attachment / 旧 GPU 计划合同，迁移时应从公开 Inference
请求中移除；删除后必须保留 proto 字段编号和名称（使用 `reserved`），不能复用：

| 位置 | 旧字段 | 处理 |
| --- | --- | --- |
| `CreateInferenceServiceRequest` | `gpu_owner_attachment = 10` | 退休；由 Inference 根据 `ResourceSpec.gpu` 调 ACC 解析，并持久化解析结果 |
| `CreateInferenceServiceRequest` | `original_charges = 11` | 退休；配额账本由 Governance/ACC 新合同提供 |
| `DeleteInferenceServiceRequest` | `gpu_owner_attachment = 4` | 退休；删除时使用持久化的 GPU 关联和 Inference 本地 absence 证据，不调用 ACC |
| `DeleteInferenceServiceRequest` | `original_charges = 5` | 退休；退款依据改为新账本合同 |
| `OperationResponse` | `durable_owner_ack = 3`（旧 integration 包） | 退休或替换为 `accelerator.command.v1.DurableOwnerAck`，迁移前不能改变 wire 编号语义 |
| 持久化/内部对象 | `GpuPlan`、`GpuPlanDigest`、`ResolvedGpuPlan`、`GpuChargeRef` | 不再作为 ACC main 的输入输出；迁移期可读旧数据，但不得用旧类型生成新请求 |

这些字段的退休是后续 Inference/Governance 改造任务，不在本基线任务中实施。

## Governance 和 Model 依赖核对

Governance `main` 提交 `90b1ecc` 已删除 Inference 管理 proto、生成代码、HTTP
路由、Inference client 和服务实现；其 README/AGENTS 也明确本期暂不提供
Inference 管理接口。因此当前 Inference 对 Governance 的管理 API 依赖不能按
旧的 `admin/service/v1/i_inference.proto` 继续生成，后续需要按新设计重新对接。

Model `master` `430d35a` 仍提供独立的 `model.v1.ModelService`，包括模型、模型
版本、导入任务、导入日志、外部模型搜索和下载 URL。Inference 只需通过 Model
引用 `model_version_id` 及已确认的运行时信息；本基线没有发现 Model 依赖
`accelerator/integration/v1` 或 Governance Inference 管理 proto。

## 基线验证

在本工作树执行：

```text
scripts/generate-api.sh lint
```

结果：直接执行返回 `126 (Permission denied)`，原因是脚本当前没有可执行位；用
等价命令执行：

```text
bash scripts/generate-api.sh lint
```

结果：退出码 `0`。

编译基线：

```text
go test ./... -run '^$'
```

结果：退出码 `0`，23 个包完成编译检查；没有执行测试函数。
