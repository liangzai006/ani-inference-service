# 2026-09-14 当前集群测试预检

用户指定使用开发机 kubectl 的当前集群。所有集群请求显式固定
`--context=kubernetes-admin@kubernetes`，未切换本地 context。

## 实际证据

- pass：3 个节点 Ready：dev-phys-02、dev-phys-03、kubercloud。
- dev-phys-02 / kubercloud 各申报 allocatable `nvidia.com/gpu=2`，
  dev-phys-03 为 0；三个节点另有 Volcano vGPU 扩展资源。
- 非终止 Pod 查询未发现 `nvidia.com/gpu` 请求。这是 Kubernetes 请求快照，
  不能证明物理 GPU 空闲、设备插件可用或与 vGPU 分配互不冲突。
- pass：LWS CRD 已 Established，服务版本为 v1。
- pass：lws-system/lws-controller-manager 有 1 个 ready 副本，镜像为
  `registry.k8s.io/lws/lws:v0.10.0`，与本仓库依赖版本一致。
- InferenceService CRD 尚不存在。
- pass：`kubectl --context=kubernetes-admin@kubernetes --request-timeout=20s apply --dry-run=server -f config/crd/bases/ani.kubercloud.com_inferenceservices.yaml`
  返回 `created (server dry run)`。仅验证 API 接受该定义，未实际安装。

## 下一阶段范围

拟使用专用 namespace `ani-inference-e2e-20260914` 和隔离测试数据库/租户。
先验证 CPU Deployment/Service、真实 Pod readiness 和本机 worker + PG 恢复；
再验证现有 LWS controller 的运行行为；引擎/GPU 用例需固定镜像、模型制品及
设备使用范围。测试 provider 只能证明本地流程，不能替代真实配额或发布链路证据。

完整 operator 测试需要安装仓库现有 Inference CRD，这是集群级新增资源。
此前禁止集群变更的约束尚未明确放开至 CRD 安装，本轮未执行安装、测试 workload
创建、现有资源接管或删除。当前所有运行时和引擎测试仍为 not_verified。
