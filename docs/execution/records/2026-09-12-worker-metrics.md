# Durable worker metrics slice

已在 `internal/biz/work/metrics.go` 增加低基数 OpenTelemetry 计数器：

- `ani_work_claim_total`
- `ani_work_claim_empty_total`
- `ani_work_completed_total`
- `ani_work_retry_total{error_class=...}`
- `ani_work_failure_total{error_class=...}`

`error_class` 只允许固定类别，不包含 tenant、service、operation 或 request ID。
Worker 在 claim、执行、retry、commit 和 stale generation 路径记录计数；指标不改变
持久状态和重试语义。单元测试覆盖错误分类边界。

验证：

```text
GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/biz/work -count=1
```

结果：`pass`。PG backlog 深度、最老工作年龄和 observation stale 已由
`WorkStore.MetricsSnapshot` 及正式 observability gauge 接入，使用全局聚合、不含
租户或资源标签；模型加载时延和依赖错误细分仍需从领域阶段接入。
