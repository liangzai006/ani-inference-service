# 2026-09-12 applied generation projection

## 修正

规格逻辑模型要求同时表达 desired generation 和 applied generation。新增
`000010_applied_generation.sql`，在 `inference_services` 增加
`applied_generation`，初始为 `0`。普通 service desired-state 事务只推进
`desired_generation`；只有 observation CAS 成功后，才在同一 PostgreSQL 事务
中以 `GREATEST` 推进 applied generation。

lease-fenced worker 路径和兼容的非 lease 观察入口都使用单事务，避免
observation 与 applied projection 因进程退出而分裂。

这表示“该代 runtime 观察已提交”，不表示 runtime ready、模型已加载、发布
已生效或调用健康；这些事实仍由各自 projection/字段表达。

`InferenceService.applied_generation` 已加入 v1 gRPC 只读消息，并由 GET/LIST
从同一 tenant-scoped PostgreSQL projection 返回；Create/Update 入参和命令
状态机没有改变。

## 证据

- `TestPostgresCreateLifecyclePreservesQuotaResources` 在真实本机 PostgreSQL
  中断言 create 完成后的 service row 为 `applied_generation = 1`。
- `sqlc generate`、PostgreSQL 包测试、全量 Go 测试、vet 和 build 通过。
- gRPC/PG projection 测试确认初始服务返回 `applied_generation = 0`。
