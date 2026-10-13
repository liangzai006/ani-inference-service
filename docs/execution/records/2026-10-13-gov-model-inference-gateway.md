# 新集群 Gov → Model → KServe → Higress 验证（2026-10-13）

本记录只针对 `.clusterenv` 对应的新集群 `kubernetes-admin@kubekey`，旧环境未连接、未修改。
部署使用 Helm；没有使用业务安装脚本。

## 验证链路

1. 通过 Governance 租户 API 查询 ModelScope 导入任务。
2. 通过 Governance 查询已就绪 Model 版本。
3. 通过 Governance 创建 CPU KServe 推理服务，完整保留请求传入的 vLLM `command` 和 `args`。
4. Inference 创建 KServe `InferenceService`、模型物化 Job/PVC 和 Higress 专属 HTTPRoute。
5. 通过 Higress `POST http://192.168.102.68:30090/v1/completions` 调用，模型名只放在 JSON
   `model` 字段中，不发送内部路由 Header。
6. 通过 Governance 删除推理服务并清理本次物化资源。

## 结果

- ModelScope 导入任务查询结果为 `completed`，进度 100%，attempt 2；Model 服务更新后，
  已完成任务不会再向外返回第一次失败留下的旧 `error_message`。
- Model 版本为 `ready`，大小 `999602688` 字节，制品路径为 Model 服务生成的 S3-compatible
  存储路径，SHA256 为 `e1fc761d399f82372857717c6b38c87ad238f9dc85cf033111b7b2d07bb89e1c`。
- Gov 创建操作 `f2205131-480d-41af-9254-3d7a8dc7928a` 达到 `succeeded/complete`；KServe
  预测器就绪，Higress 路由状态为 `Accepted=True`、`ResolvedRefs=True`。
- Higress 缺少 `x-api-key` 返回 `401`；带 key 且 JSON `model=qwen2-0-5b-flow-210455`
  的请求返回 `200` 和非空 completion；带 key 的未知模型请求返回 `500`，没有落到已部署模型。
- Gov 删除操作 `3910acdf-a956-4021-a4a0-d3cf93312104` 达到 `succeeded/complete`。
  KServe 对象、预测器、业务 HTTPRoute、物化 Job/PVC/下载 Secret 和完成的迁移 Job 已清理。

## Higress 路由修复

仅发布模型 Header 匹配的 HTTPRoute 时，首个没有内部 Header 的请求没有可匹配路由，
model-router 无法执行。Inference Chart 现在安装一条只匹配 `/v1/completions`、不配置
`backendRefs` 的兜底 HTTPRoute。插件从 body 读取 `model` 后，Higress 重新计算路由并进入
模型专属 HTTPRoute；未知模型保留在无后端规则上，由网关拒绝。调用方不需要、也不应手工
构造 `x-higress-llm-model`。

## 资源状态

验证结束时 `ani-system` 只保留 Governance、Model、Inference 三个 Running Deployment，
以及 Inference Chart 管理的兜底 HTTPRoute；没有 KServe 推理实例、Job、PVC 或异常 Pod。
集群当前没有 GPU 节点，因此本记录不包含 GPU、多机多卡或 Accelerator 验证。
