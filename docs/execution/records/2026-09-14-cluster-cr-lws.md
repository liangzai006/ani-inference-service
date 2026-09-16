# 2026-09-14 当前集群 CRD 与 LWS 验证

## 环境

- kube context：`kubernetes-admin@kubernetes`
- namespace：`ani-inference-e2e-20260914`
- LWS controller：`registry.k8s.io/lws/lws:v0.10.0`
- InferenceService CRD：`ani.kubercloud.com/v1`

## 结果

1. 项目 InferenceService CRD 已安装，`Established=True`。
2. 创建 stopped 的 `InferenceService/inference-e2e-cr` 成功，API schema 接受
   generation、modelVersionId、resource、replicas 和 deployment runtime 字段。
3. 创建一个 CPU `LeaderWorkerSet/inference-e2e-lws`，设置 1 个 group、每组 3 个
   进程；LWS controller 成功创建 StatefulSet。
4. 3 个 Pod 均 `Running/Ready`，实际分布在 `dev-phys-02`、`kubercloud` 和
   `dev-phys-03`，证明当前集群的 LWS 多节点 handoff 和调度可用。
5. 删除测试 CR/LWS，等待 StatefulSet/Pod 消失；专用 namespace 无残留 workload。

## 未覆盖范围

本次没有启动正式 Inference worker，也没有使用 GPU、模型制品、真实 quota、IAM、
publication 或数据面。因此这些能力仍标为 `not_verified`；CRD 安装本身不等同于
Inference operation 已执行。
