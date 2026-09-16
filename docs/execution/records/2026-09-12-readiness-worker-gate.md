# 2026-09-12 PostgreSQL-backed readiness worker gate

## 问题

正式进程在配置 `ANI_DATABASE_DSN`、但没有启用 Kubernetes background server 时，
原先把 `len(background) == 0` 当成初始 ready。这样会让服务接受并持久化 operation，
却没有持久 worker 执行它，违反“落库后必须持续恢复”的运行合同。

## 修复

`initialReadiness` 现在只允许真正的 dependency-free layout 进程初始 ready。配置了
PostgreSQL 但没有 worker 时保持未 ready；配置 background worker 时等待 worker 自己的
依赖门禁和 `ReadyCheck` 回调。

新增测试：

- `TestInitialReadinessDoesNotAcceptPersistedCommandsWithoutWorker`
- `TestInitialReadinessAllowsDependencyFreeProcess`

## 验证

在开发机 PostgreSQL 容器内启动当前静态正式进程，使用 Unix socket 连接现有
`ani_inference` 数据库：

- `/healthz` 返回 HTTP 200；
- `/readyz` 返回 HTTP 503；
- 临时进程随后收到 SIGINT 并停止。

随后执行全仓 `go test -p 1 ./...`、`go vet -p 1 ./...`、`go build -p 1 ./...`、
`sqlc generate`、Buf lint 和 `git diff --check`，均通过。未修改数据库、集群、旧 ANI
或远程仓库。
