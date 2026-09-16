# 2026-09-14 当前集群 GPU 验证

在 `kubernetes-admin@kubernetes` 的专用 namespace
`ani-inference-e2e-20260914` 创建唯一 GPU Pod：

```yaml
resources:
  requests:
    nvidia.com/gpu: "1"
  limits:
    nvidia.com/gpu: "1"
```

结果：Pod 调度到 `dev-phys-02`，状态 `Running/Ready`；容器内执行
`nvidia-smi -L` 成功发现 `NVIDIA GeForce RTX 4090`。随后删除 Pod 并确认专用
namespace 无残留 Pod。

这证明设备插件、整卡扩展资源和 GPU 调度可用。Inference Deployment/LWS 的 GPU
渲染已在源码测试覆盖，但真实引擎、配额结算、模型加载和 operation 恢复尚未由本次
GPU smoke 证明。
