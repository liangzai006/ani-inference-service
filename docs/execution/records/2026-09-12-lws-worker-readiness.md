# 2026-09-12 LWS worker readiness

## 发现

LeaderWorkerSet 的 `ReadyReplicas` 表示 ready groups，不表示每个 leader/worker Pod 都 ready。此前 runtime observer 只写入 `ReadyGroups`，`ReadyWorkers` 始终为零，却仍可能把整个 LWS 判为 ready。

## 修正

LWS 观察现在通过 client-go/controller-runtime typed `PodList` 查询同租户、服务、generation 和 LWS name 标签的 Pod，使用官方 `sigs.k8s.io/lws` 的 `WorkerIndexLabelKey` 识别 worker，并按 `PodReady=True` 计数。runtime 只有在 LWS controller generation/updated groups、ready groups 和期望 worker 数都满足时才报告 ready；leader/group readiness 不再替代 worker readiness。

## 证据

- `TestRuntimeExecutorObservesLeaderWorkerSetReadiness` 验证两个 groups、六个 ready workers 的独立计数。
- `TestRuntimeExecutorDoesNotTreatReadyLWSGroupsAsReadyWorkers` 验证 groups ready 但 worker 缺失时不会报告 ready。
- Kubernetes 包测试、完整 Go 测试、vet 和 build 通过。
- 真实 LWS controller、Pod lifecycle、GPU scheduling 仍为 `not_verified`。
