# Task 2 report

## 实施内容

- `api/inference/v1/inference.proto` 改为消费 ACC main 的
  `accelerator/v1` 和 `accelerator/command/v1`。
- 新增 transport-neutral `CommandContext`，并为 Create/Update 增加
  `cluster_id`、`gpu_execution`、`gpu_display` 冻结快照字段。
- Delete 保留 `create_operation_id`，旧 `gpu_owner_attachment`、
  `original_charges` 字段号和名称全部 `reserved`；未实现旧新双读或别名。
- `OperationResponse.durable_owner_ack` 改为
  `accelerator.command.v1.DurableOwnerAck`，保持字段编号 3。
- 新增 API 子模块 `api/go.mod`/`api/go.sum`，固定 ACC main 版本
  `v0.0.0-20261018033121-4f084c9f5f25`，保留原 `go_package`/import 路径。
- 生成脚本从 API 子模块读取 ACC 精确版本，并挂载该版本的 proto；脚本补齐可执行位。
- 增加命令上下文、执行摘要、集群 UUID、GPU 执行资源键和旧字段未知值校验测试。

## 验证

```text
./scripts/generate-api.sh lint                         PASS
./scripts/generate-api.sh generate                     PASS
./scripts/generate-api.sh generate                     PASS (第二次无差异)
GOWORK=off go -C api test ./...                        PASS
git diff --check                                        PASS
```

根模块的编译门禁暂未通过，原因属于 Task 3 运行时迁移边界：

```text
internal/data/postgres/... imports accelerator/integration/v1
internal/data/accelerator/plan_adapter.go references ResolvedGpuPlan,
MemoryEncoding, ResourceTotals and RuntimeFragment
```

这些类型已从 ACC main 删除；本任务没有修改运行时、ACC、Resource、Governance
或 Model。Task 3 必须先迁移这些适配器，不能通过恢复旧 integration 包或伪造别名绕过。
