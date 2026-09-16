# 2026-09-12 controller-runtime envtest watch

## 验证范围

使用 `setup-envtest` 下载的 Kubernetes `1.36.2` API server/etcd，并加载本
仓库 InferenceService CRD 与官方 `sigs.k8s.io/lws v0.10.0` LeaderWorkerSet
CRD。测试明确设置 `UseExistingCluster=false`，不会连接开发机现有集群。

`TestControllerEnvtestWatchNotifiesDurableBoundary` 验证：

1. 创建带租户、服务标签的 InferenceService 会调用 durable `WorkNotifier`；
2. 创建带相同标签和 generation 的 Deployment 会经 runtime map 重新入队；
3. 删除 Deployment 也会再次通知；
4. `PollInterval` 设为一小时，通知来自 watch 而不是周期 requeue。

## 结果

命令：

```text
KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.36.2-linux-amd64
LWS_CRD_PATH=/root/.config/.gvm/go/gopath/pkg/mod/sigs.k8s.io/lws@v0.10.0/config/crd/bases
GOCACHE=/tmp/ani-go-cache go test ./internal/data/kubernetes \\
  -run TestControllerEnvtestWatchNotifiesDurableBoundary -count=1 -v
```

结果：`PASS`。该证据只覆盖 API server、cache、watch 和通知映射；LWS
controller 调度、PostgreSQL 写入、真实 runtime status、GPU 和 publication
仍分别为 `not_verified`。
