# Inference service guidance

本仓库的网关边界必须保持清晰：

- Higress 只承载 OpenAI 兼容的推理调用接口，例如 `/v1/completions`。
- 模型、模型版本、导入任务和推理服务管理等 `/api/v1/...` 业务接口走
  Governance BFF，不得通过 Higress 路由或 AI 网关插件转发。
- 修改 HTTPRoute、网关插件、代理或 Helm 入口时，分别验证 Higress 的
  `/v1/...` 推理入口和 Governance 的 `/api/v1/...` 业务入口；不能用一个入口
  的 401/404 代替另一个入口的验证。
