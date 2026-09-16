# 2026-09-11 runtime executor slice

## 已验证

- `internal/data/kubernetes/renderer.go` 使用 `appsv1.Deployment`、`corev1.ResourceRequirements` 和官方 `sigs.k8s.io/lws/api/leaderworkerset/v1`，保留 Kubernetes requests/limits、GPU 扩展资源、自定义 argv 和 LWS 分布式形状。
- `internal/data/kubernetes/executor.go` 使用 controller-runtime typed client 的 server-side apply；不使用 `unstructured` 构造运行对象，不启用 ForceOwnership。
- 运行对象存在时检查 tenant/service 所有权、generation、删除中状态，并携带 UID/resourceVersion 进行并发 fencing；删除必须使用 PostgreSQL 持久 binding 的 UID/resourceVersion。
- running runtime 在配额 reservation 未确认时拒绝执行；stopped/deleted 在 publication 未撤回确认或没有持久 binding 时拒绝删除。
- `internal/server/worker.go` 将 durable `work.Loop` 接入 Kratos `Start/Stop` 生命周期，循环不保存业务队列；本轮测试覆盖启动、重复启动、停止和未配置拒绝。

## 验证命令

- `GOCACHE=/tmp/ani-go-cache go test -p 1 -mod=readonly ./internal/data/kubernetes -count=1`：pass
- `GOCACHE=/tmp/ani-go-cache go test -p 1 -mod=readonly ./internal/server ./cmd/ani-inference-service -count=1`：pass
- `GOCACHE=/tmp/ani-go-cache go test -p 1 -mod=readonly ./... -run '^$'`：pass（全仓编译）
- `GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...`：pass
- `GOCACHE=/tmp/ani-go-cache go build -p 1 ./...`：pass
- 开发机网络环境 `GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1`：pass（包含 cmd 进程生命周期测试）

## 尚未完成

LoopServer 尚未在正式进程中装配 PostgreSQL WorkStore、Kubernetes manager 和真实 RuntimeExecutor。Quota/publication provider、operation step 状态机、Kubernetes 状态解析及 observation/binding 写回仍需独立切片完成；因此本记录不能替代真实 Kubernetes、LWS、引擎和配额链路验收。
