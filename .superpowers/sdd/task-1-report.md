# Task 1 报告：ACC / Inference 合同基线

## 工作树

- 工作树：`/tmp/ani-contract-20261018/inference`
- 分支：`codex/acc-contract-20261018`
- 只修改 Inference 仓库；未修改 ACC、Resource、Governance、Model。

## 核对结果

- ACC main `4f084c9f5f2560c9ed00f59db2284c3f991db0a4` 的主包是 `accelerator.v1`，命令/持久确认包是 `accelerator.command.v1`。
- ACC main 的解析合同为 `ResolveGpuRequestRequest -> ResolvedGpuRequest`，响应包含 `GpuExecution`、`GpuQuotaItem`、`GpuDisplay`。
- ACC main 的使用合同为 `SyncGpuUsage`、`GetGpuUsage`、`ListGpuUsages`、`ListBindings`、`ObserveRelease`；`ObserveReleaseRequest` 必须含 `cluster_id`、`sealed_at`。
- ACC main 不含 `accelerator/integration/v1`。
- Inference 当前 `api/inference/v1/inference.proto` 仍导入 `accelerator/integration/v1/owner_gpu_attachment.proto`；Go 源码仍引用 `GpuOwnerCreateAttachment`、`GpuOwnerDeleteAttachment`、`ResolvedGpuPlan`、`GpuChargeRef` 和旧 `OriginalQuotaCharge`。
- Inference 当前模块实际使用 ACC `v0.0.0-20261016163330-c1242866d987`，所以还没有消费 ACC main 的新合同。
- Governance main `90b1ecc8cfa31b96ac43a8e4ff3aa18d41f03812` 已删除 Inference 管理 proto、生成代码、HTTP 路由、client 和 service；当前 main 不再提供旧 `admin/service/v1/i_inference.proto`。
- Model master `430d35aa9a18ef058ac0744cbb51e2ffe1bf47ce` 继续提供独立 `model.v1.ModelService`，包含模型、版本、导入、日志、外部搜索和下载 URL；未发现其依赖旧 ACC integration 或 Governance Inference 管理 proto。

完整字段表和退休字段表见 `docs/contracts/acc-inference-contract.md`。

## 基线命令

1. `scripts/generate-api.sh lint`
   - 退出码 `126`：文件当前没有可执行位（`Permission denied`）。
2. `bash scripts/generate-api.sh lint`
   - 退出码 `0`。
3. `go test ./... -run '^$'`
   - 退出码 `0`；23 个包通过编译检查，未执行测试函数。
4. `git diff --cached --check`
   - 通过。

## 风险和后续

当前 Inference 与 ACC main 是不兼容的合同版本。后续迁移需要修改 Inference proto、ACC client、GPU 持久化和释放流程，并同步 Governance 新的重新对接方案；本任务只冻结基线，没有改变运行时代码。
