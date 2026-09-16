# 2026-09-12：跨代配额与发布 fencing

本轮实现并验证了几个此前未闭合的持久化边界：

- 配额和 publication fact 写回携带 PostgreSQL work lease；旧 worker 不能在租约失效后覆盖当前状态。
- stop/delete 使用目标 generation 之前的 active quota reservation；更新了 SQL，使当前 operation 的 lease 可以安全释放旧 generation reservation。
- update/restart 在撤发布、runtime absence 确认后经过 `release_previous_quota`，再为新 generation Reserve/Confirm，避免双占配额。
- stop/restart/delete 的 publication 查询改为读取目标 generation 之前最新事实，避免新 operation 看不到旧 generation 的已发布路由。
- delete operation 成功事务设置 `inference_services.deleted_at`；work claim/commit/retry 排除 tombstone。
- observation 写回补充 runtime phase、LWS 计数、LWS UID 和 runtime binding objects；观察投影将 `stale_after` 更新为两分钟后的时间点。

验证：`sqlc generate`、`GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1`、`go vet`、`go build` 通过；本地 `recycling-postgres` 中应用 `000006_update_previous_quota.sql`，并以 `ANI_DATABASE_DSN` 运行 PostgreSQL 包测试通过。真实配额 authority、publication、IAM、模型物化、引擎健康、Kubernetes API server 和数据面仍为 `not_verified`。
