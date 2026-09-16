# 2026-09-12 runtime boundary follow-up

本轮完成两项边界收敛：

- `InferenceService` control binding 使用现有 tenant-scoped runtime binding CAS 表，CR apply 后立即记录 UID/resourceVersion；删除前必须匹配持久 control binding。
- 本地 admission adapter 从 PostgreSQL immutable spec 校验 runtime 形状后再进入外部 quota/runtime provider。

Invocation health 已有显式 `InvocationProbe` 端口和 HTTP adapter，当前尚未接入主进程的真实运行时 endpoint；`invocation_health=unknown` 只表示尚未取得调用探测事实，不能把 runtime ready 当作调用健康。真实 probe、publication/data-plane 和外部 provider 仍为 `not_verified`。
