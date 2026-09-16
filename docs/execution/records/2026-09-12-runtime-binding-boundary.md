# 2026-09-12 runtime binding boundary

## 发现

运行时 apply 成功后，旧流程要等 `ObserveRuntime` 才把 UID/resourceVersion 写入 PostgreSQL。进程若在这两个步骤之间退出，重试会看到 Kubernetes 对象已存在，却没有可用于 fencing 的持久 binding，因而无法安全继续或接管。

## 修正

`internal/data/kubernetes.RuntimeExecutor.applyFenced` 现在在 typed Deployment/LWS server-side apply 成功后，使用 controller-runtime manager 的 uncached `APIReader` 重新读取对象，再通过窄接口立即写入 PostgreSQL binding。写入包含 tenant、service、generation、kind、namespace、name、UID、resourceVersion、role，并在已有对象更新时携带旧 UID/resourceVersion 作为 CAS 预期值。主进程 composition 将 `postgres.Repository` 注入该接口。

APIReader 是必要的：manager cache 可能落后于刚刚成功写入的 API server，不能在这个 crash-sensitive 边界使用缓存作为身份来源。后续观察仍负责 readiness/status；binding 已在 apply 边界持久化，不再依赖观察步骤才能恢复。

## 证据

- 先加入 `TestRuntimeExecutorPersistsBindingImmediatelyAfterApply`，确认 apply 成功后立即收到非空 UID/resourceVersion；生产实现前该测试按预期因缺少接口而失败。
- `TestRuntimeExecutorFailsWhenBindingPersistenceFails` 确认 PostgreSQL binding 写入失败会从 `ApplyRuntime` 返回，不能被吞掉或当作运行完成。
- `GOCACHE=/tmp/ani-go-cache go test ./internal/data/kubernetes ./internal/data/postgres -count=1` 通过。
- 开发机 PostgreSQL 环境执行 `GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1` 通过（cmd 4.251s，其他包通过）。
- 真实 Kubernetes API server、LWS 和进程级 crash 恢复仍为 `not_verified`；fake client 只验证边界调用和 CAS 数据形状。
