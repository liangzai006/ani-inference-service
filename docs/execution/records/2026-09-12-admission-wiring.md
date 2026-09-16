# 2026-09-12 admission wiring

## 修正

新增 PostgreSQL-backed local `Admission` adapter。它从当前 tenant/service/generation 的 immutable spec 读取 runtime mode、replicas、worker replicas 和 Kubernetes resources，调用领域 `admission.ValidateCreate`，在 quota/runtime provider 之前拒绝非法形状。该 adapter 不复制 IAM、租户权限或 Core quota policy。

主进程 operation runner 现在注入该 adapter；外部 quota、publication、model 和 invocation provider 仍然必须单独配置，readiness gate 不会因为本地 admission 存在而提前开放。

## 证据

- `TestAdmissionValidatesPersistedRuntimeShape` 验证合法 deployment spec 通过、deployment replicas 不合法时拒绝。
- 开发机环境 `go test -p 1 ./... -count=1`、`go vet -p 1 ./...`、`go build -p 1 ./...` 通过。
- 真实 IAM、Core quota、Model、publication 和 Kubernetes provider 仍为 `not_verified`。
