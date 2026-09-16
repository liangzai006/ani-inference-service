# Model 原型契约兼容记录

## 只读核对结果

旧 ANI 原型的 Model gRPC 已提供 `GetModelVersion` 和 `GetModelDownloadURL`，并包含
`model_id`、`version`、`format`、`storage_path`、`checksum_sha256`、加密字段及版本状态。
这些字段覆盖 Inference 获取模型制品引用的最小需求。

## 兼容决策

新 Model 服务保留上述方法和字段语义，不复制旧实现代码。运行时默认配置采用向后兼容的可选新增字段：

- `engine_type`：`vllm`、`sglang` 等；
- `startup_command`；
- `startup_args`。

Inference 将 `storage_path`/`checksum_sha256` 映射为自己的
`artifact_ref`/`artifact_sha256` 快照，并把 Model 返回的默认启动配置合并到自己的
`engine_runtime`/`command_argv`。模型导入状态属于 Model；模型加载、runtime ready 和
调用健康属于 Inference。

## 证据等级

- `pass`：旧 ANI Proto/OpenAPI 已只读核对；当前 Inference 字段可承载兼容映射。
- `not_verified`：新 Model 服务的正式 Proto 包名、endpoint、鉴权和运行时配置字段尚未实现并联调。
