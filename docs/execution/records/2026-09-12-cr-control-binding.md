# 2026-09-12 CR control binding

## 发现

`DeleteCR` 原先按 namespace/name 读取 `InferenceService`，再用当前对象 UID/resourceVersion 删除。若同名 CR 被替换，只有租户/service 标签不足以证明它仍是本服务本代 apply 的对象。

## 修正

新增 migration `000009_inference_cr_binding.sql`，允许现有 tenant-scoped binding 表记录 `object_kind=InferenceService`、`role=control`。typed CR apply 成功后使用 uncached APIReader 读取服务端 UID/resourceVersion，并以 CAS 写入 control binding；已有 CR 没有持久 control binding 时拒绝接管。DeleteCR 要求 control binding 存在且 UID/resourceVersion 完全匹配，再使用 Kubernetes preconditions 删除。

## 证据

- `TestRuntimeExecutorPersistsCRBindingImmediatelyAfterApply` 验证 CR apply 边界立即持久化 control binding。
- `TestRuntimeBindingCASRejectsStaleUIDAndResourceVersionIntegration` 在开发机 PostgreSQL 验证 `InferenceService/control` binding 及原有 runtime CAS 约束。
- 开发机 PostgreSQL 已应用 migration 000009；完整 Go 测试、vet、build 通过。
- 真实 Kubernetes API server、CR replacement race 和正式进程 crash 恢复仍为 `not_verified`。
