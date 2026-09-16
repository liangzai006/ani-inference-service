# Model 服务契约边界

## 决策

Model 服务不返回或维护 Inference runtime 的 `model_materialization_state`。Model 服务拥有模型目录、版本、制品和默认运行参数；Inference 拥有模型在某个推理服务 generation 中的物化、加载、runtime ready 和调用健康状态。

Model 服务返回的正式模型版本快照至少包含：

- `model_id`
- `model_version_id`
- `artifact_provider`
- `artifact_ref`
- `artifact_sha256`
- `engine_type`（例如 `vllm`、`sglang`）
- `startup_command`
- `startup_args`

Inference 将默认引擎配置与请求允许的覆盖合并后，保存最终 `engine_runtime`/`command_argv` 到自己的 desired/applied spec，并负责把它渲染到 Deployment/LWS。现有 `model_ready_known`、runtime observation 和 invocation health 仍属于 Inference 的运行事实，不是 Model 服务状态。

## 当前证据

- `pass`：Inference schema 已有 artifact、`engine_runtime`、`command_argv` 和独立 model readiness fact。
- `not_verified`：Model 服务 gRPC proto、endpoint、鉴权和真实模型制品协议尚未提供。
