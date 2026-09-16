# 2026-09-11 operation step CAS slice

## 已验证

- `queries/inference.sql` 增加 `AdvanceOperationStepCAS` 和 `RetryOperationStepCAS`。
- Advance 同时限定 `tenant_id`、operation、target generation、expected phase/step、service current operation 和 desired generation；提前 retry 会被数据库拒绝。
- Retry 将 phase/attempt/retry_at/error 和 lease 清理持久化，保留当前 step，进程重启后可由 durable scan 继续处理。
- `internal/biz/inference/operation.go` 约束 admission、quota、CR、runtime、observe、publication、release 和 terminal step 的合法转移；失败统一落到 `complete` 标记。
- `internal/data/postgres/operation_state.go` 提供 tenant-scoped repository CAS，并在 0 行更新时返回 `ErrOperationCAS`。
- migration `000004_operation_runtime_steps.sql` 已将 `delete_runtime` 和 `observe_absence` 加入持久 step 约束，并已在本地 `ani_inference` 数据库应用。

## 验证命令

- `sqlc generate`：pass
- `gofmt -w internal/biz/inference/operation.go internal/biz/inference/operation_test.go internal/data/postgres/operation_state.go internal/data/postgres/operation_state_test.go`：pass
- `GOCACHE=/tmp/ani-go-cache go test ./internal/biz/inference ./internal/data/postgres`：pass
- 开发机网络环境 `INFERENCE_PG_DSN=postgres://recycling_user:recycling_pass@localhost:5432/ani_inference?sslmode=disable GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/data/postgres -run TestOperationStepCASIntegration -count=1`：pass

## 尚未完成

该切片只提供本地持久状态推进和 CAS，不调用或伪造 quota/publication/runtime provider。正式 operation worker、provider 协议、Kubernetes 观察写回和进程 manager 装配仍需后续切片完成。
