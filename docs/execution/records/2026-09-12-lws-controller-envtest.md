# 2026-09-12 LWS controller envtest

## 目的

在隔离 Kubernetes API server 上启动官方 `sigs.k8s.io/lws` controller，验证
Inference 生成的 typed `LeaderWorkerSet` 能交给官方 controller 创建其拥有的
StatefulSet。

## 结果

`TestOfficialLWSControllerCreatesStatefulSetEnvtest` 直接使用生产
`LeaderWorkerSet` renderer，并使用本机缓存的 Kubernetes
1.36.2 envtest assets 和 LWS v0.10.0 CRD/controller，通过。测试观察到官方
controller 为一个 group、3 个 worker 的 LWS 创建 1 个 StatefulSet，并验证
StatefulSet replica 与 serviceName 已生成。

此前测试暴露了一个真实渲染问题：envtest 不运行 LWS webhook，而官方 controller
会直接解引用 `RolloutStrategy.RollingUpdateConfiguration`。Inference renderer
因此显式写入官方 webhook 的默认值（`maxUnavailable=1`、`maxSurge=0`、
`partition=0`），不再依赖 webhook 默认化。

## 边界

envtest 不运行 kube-controller-manager、scheduler、device plugin 或真实 Pod，
所以 StatefulSet 到 Pod 的创建、GPU 调度、LWS 多节点 readiness、真实集群 CRD
安装和引擎调用仍是 `not_verified`。本证据也不替代正式集群上的 LWS 版本兼容性
和 admission/webhook 验证。
