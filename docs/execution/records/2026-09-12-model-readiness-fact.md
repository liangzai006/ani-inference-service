# 2026-09-12 model readiness fact

## 发现

`model_ready=false` 既可能表示模型尚未被观察，也可能表示 provider 已确认模型不可用。Kubernetes runtime observer 只知道 Pod/Deployment/LWS 状态，不能据此推断模型事实；因此原字段无法让重启后的 runner 正确区分 unknown 与 confirmed false。

## 修正

新增 migration `000008_model_readiness_fact.sql`，为 `inference_runtime` 增加 `model_ready_known`。`MarkModelMaterializingForWork` 清除 ready 和 known；`SaveModelObservation` 只在 Model provider 返回 `Known=true` 时将 known 置为 true。RuntimeSource 读取该持久事实，Kubernetes executor 返回 runtime readiness 时合并 model ready/known，不伪造模型结果。

## 证据

- 真实 PostgreSQL `TestReconcileStoreObservationRequiresCurrentLease` 验证物化开始后 known=false，并验证 provider 返回 `Known=true, Ready=false` 后持久化为已知的未就绪事实。
- `sqlc generate`、Kubernetes/PostgreSQL 包测试和完整 `go test -p 1 ./... -count=1`、`go vet`、`go build` 通过。
- 真实 Model/Storage provider、调用探针和引擎仍为 `not_verified`；本切片只完成事实模型和跨组件合并边界。
