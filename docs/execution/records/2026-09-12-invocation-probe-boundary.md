# 2026-09-12 invocation probe boundary

## 修正

`RuntimeExecutor` 新增显式 `InvocationProbe` 端口。Kubernetes observer 只计算 Deployment/LWS/Pod runtime readiness；配置探针时，探针单独返回 `InvocationKnown`、`InvocationHealthy` 和原因，executor 再把它们与 runtime/model facts 组合返回给 operation runner。未配置探针时保持 unknown，不能因为 Pod ready 就发布。

## 证据

- `TestRuntimeExecutorUsesInvocationProbeSeparatelyFromRuntimeReadiness` 验证 runtime ready、持久 model ready 和 probe 健康分别成立后才组合为完整事实。
- Kubernetes 包测试、全量 Go 测试、vet 和 build 通过。
- 真实 HTTP/gRPC data-plane probe、publication URL/Service endpoint 合同和引擎调用仍为 `not_verified`。
