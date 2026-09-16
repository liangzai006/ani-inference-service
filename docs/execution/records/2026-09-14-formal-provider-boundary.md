# 正式 Model/Quota provider 边界

## 决策

固定模型、固定配额和 `development profile` 不属于正式运行方案。Inference 不创建模型目录或配额账本，也不伪造外部成功结果；它只持久化外部权威返回的模型版本/制品引用、配额 reservation/settlement 引用及状态。

正式接入必须使用版本化的 Model、Quota（以及 publication/data-plane、IAM）服务契约，包含调用身份、tenant/resource scope、幂等键、超时、重试、结果查询、`not_found` 分类和补偿语义。外部 endpoint、协议和凭据未提供或未通过真实依赖验证前，生产组合保持未就绪。

测试中的 fake provider 仅用于单元/隔离测试，不能作为开发环境成功、配额结算、模型加载或发布完成的证据。

## 证据等级

- `pass`：当前代码已有 provider 接口、持久状态机和未就绪门禁。
- `not_verified`：真实 Model/Quota endpoint、协议、鉴权、结算回查和模型制品可用性。
