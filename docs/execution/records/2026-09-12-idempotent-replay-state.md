# 2026-09-12：幂等重放返回持久状态

命令和 update 的幂等重放现在从 PostgreSQL 读取原 operation 行，并返回其真实 `phase`、`step`、错误码、错误消息和完成信息，而不是重新构造 `pending` 响应。这样请求响应丢失、客户端重试或 worker 已经完成时，重放结果与 `GetOperation` 保持一致。

验证：定向 PostgreSQL 包测试、全量 Go 测试、`go vet` 和 `go build` 通过。真实消费者契约兼容性仍需在 API 接线阶段验证。
