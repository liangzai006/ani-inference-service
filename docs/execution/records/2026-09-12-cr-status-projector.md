# CRD status projection slice

## 已完成

- 新增 `internal/data/kubernetes/status_projector.go`，从只读
  `StatusProjectionSource` 读取 PostgreSQL 投影，通过 APIReader 获取最新 CR，
  使用 `Client.Status().Patch` 只写 status 子资源。
- `status.observedGeneration` 明确取持久化的 `applied_generation`，不使用
  Kubernetes `metadata.generation` 伪造业务观察代数。
- 状态投影包含 runtime、model、publication、invocation 和当前 operation；不推进
  operation、不写 PostgreSQL、不改变 desired spec。
- fake client 测试覆盖租户/服务标签读取、状态字段投影和无归属 CR 拒绝；真实
  本机 PostgreSQL 测试覆盖 source 从 service/runtime/publication/operation 读取
  租户限定的状态快照。
- envtest 测试已加入真实 API server/status 子资源的写回断言；该测试仅在提供
  `KUBEBUILDER_ASSETS` 和官方 LWS CRD 时运行，当前开发机未提供这些环境变量，
  因此仍记为 `not_verified`。

## 已接线与限制

PostgreSQL 的 `StatusProjectionSource` adapter 和 Controller 的 status projector
装配已经完成。正式进程仍需在真实 API server 上验证 status 子资源写回；CRD
status 语义需要在外部消费者确认后冻结，因此该部分仍标记 `not_verified`。

验证：

```text
GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/data/kubernetes -count=1
INFERENCE_PG_DSN=postgres://recycling_user:recycling_pass@localhost:5432/ani_inference?sslmode=disable \
  GOCACHE=/tmp/ani-go-cache go test -p 1 ./internal/data/postgres -run TestStatusProjectionSourceReadsTenantScopedAggregate -count=1
```

结果：上述两项均为 `pass`；真实 API server/status 子资源写回仍为 `not_verified`。

## 后续更正（2026-09-12）

上面的 `not_verified` 是该记录生成时的阶段性状态。随后在开发机使用 Kubernetes
1.36.2 envtest assets 和官方 LWS v0.10.0 CRD 运行 `internal/data/kubernetes` 全量
测试，`TestStatusProjectorEnvtestConcurrency` 和 status 子资源写回相关测试已通过。
当前真实隔离 API server 的 status projection、resourceVersion 冲突重读、旧 UID/租户
拒绝证据为 `pass`；正式共享集群和外部消费者的 `ObservedGeneration` 合同仍未验证。
