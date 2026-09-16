# 2026-09-14 当前集群 CPU smoke

用户明确授权使用当前 kube context。测试固定为
`kubernetes-admin@kubernetes`，资源位于专用 namespace
`ani-inference-e2e-20260914`。

## 步骤与结果

1. 安装项目自有 `inferenceservices.ani.kubercloud.com` CRD，状态 `Established=True`。
2. 创建专用 namespace；现有 LWS controller 为 `v0.10.0` 且 ready。
3. 使用集群已有镜像 `docker.changqingyun.cn/.../nginx:latest` 创建唯一 CPU
   Deployment/Service，requests `10m/32Mi`，limits `100m/64Mi`。
4. Deployment rollout 成功；Pod 在 `dev-phys-02` 达到 `1/1 Running`，Service
   ClusterIP 为 `10.108.204.142`，Endpoints 指向 Pod `10.16.0.165:80`。
5. 删除该 Deployment/Service，等待 Pod 消失；namespace 最终无资源。

## 边界

这是当前集群的真实 Kubernetes API、调度、Pod readiness 和 Service endpoint 证据，
不是 Inference engine 或业务 operation 证据。GPU 节点申报了 `nvidia.com/gpu=2`，但
真实设备插件、GPU 调度、多节点 LWS、模型加载、调用健康和 quota/provider 仍需后续
专用测试。没有接管现有业务资源。
