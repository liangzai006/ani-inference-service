# 2026-09-12 持久 operation runner 与 PG store

## 已完成

- `internal/biz/inference/runner.go` 按持久 step 调用 admission、quota、CR/runtime、publication adapter；provider 错误和未就绪状态写回可重试 operation。
- 运行成功门槛分开检查 runtime ready、model ready、invocation healthy；发布确认后才完成 operation。
- stop/delete 固定执行撤发布确认 → 删除 runtime → 确认 runtime 消失 → 释放 quota；update/restart 在旧 runtime 消失后才重新预留配额。
- `internal/data/postgres/operation_store.go` 通过 `resource_work` lease 和 generation 读取当前 operation，保存 quota/publication/runtime 事实，并提供 operation CAS + audit 的同事务实现。
- operation CAS/retry 查询同时检查 `resource_work.lease_token` 和 lease expiry，旧 worker 无法推进新租约的 operation。
- start/restart/update 受理时为目标 generation 写入 pending quota reservation；stop/delete 通过 generation 范围读取待释放的旧 reservation。

## 证据

- `sqlc generate`：pass。
- `GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1`：pass。
- `GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...`、`go build -p 1 ./...`：pass。
- 本机 PostgreSQL `TestOperationStoreLoadsLeaseFencedOperation`、`TestOperationStepCASIntegration`、`TestReconcileStoreObservationRequiresCurrentLease`：pass。

## 尚未完成

- Core quota 的真实 Reserve/Confirm/Release 协议、publication provider、Model 制品物化和引擎调用探针尚未接入。
- `cmd/ani-inference-service/main.go` 已按 active operation 选择 runner、按 terminal operation 选择持续观察 reconciler；缺失 quota/publication/admission provider 时保持 durable retry。真实 Kubernetes/LWS 调度和产品链路仍标记 `not_verified`。
- RuntimeSource 会从 Inference 自有 reservation/publication projection 恢复运行门禁；runtime 观察在 provider 未提供模型/调用事实时保留已有 PG 值，避免周期巡检覆盖已确认健康状态。
- `delete` 增加 `delete_cr` 持久步骤；仅在 runtime absence 确认后，按 CR 的 UID/resourceVersion 删除 InferenceService 投影，随后才释放 quota。
