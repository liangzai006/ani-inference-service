# Model 版本引擎默认值联动设计

## 目标

创建推理服务时，用户先选择 `model_version_id`，前端从 Model 服务读取该版本的
引擎类型、镜像和完整启动命令并自动填入表单。创建请求仍然携带这些字段，允许
用户在本次推理中临时修改；推理服务不拼接、拆分或重写启动命令。

## 现状

Model 已经在 `ModelVersion` 和 `CreateModelVersionRequest` 中提供并持久化：

- `engine_type`
- `engine_image`
- `startup_command`

Inference 当前从 Model 读取版本就绪状态和制品信息，创建请求已经有对应的引擎
字段。Inference 的引擎字段仍然属于本次创建请求，Model 的三个字段是前端回填默认
值，不是 Inference 运行时自动覆盖请求的来源。

## 方案

1. 前端选择模型版本后调用 Model 的版本查询接口。
2. Model 返回版本元数据和三个引擎默认字段；前端将它们填入创建表单。
3. 前端提交 `model_version_id`、资源参数以及 `engine.type`、`engine.image`、
   `engine.command`。
4. Inference 通过 Model 校验版本属于当前租户且状态为 `ready`，校验制品路径和
   校验和。
5. Inference 持久化创建请求中的引擎字段；服务端不从 Model 自动覆盖请求值，也
   不在请求缺字段时拼接或猜测启动配置。
6. KServe/LWS 渲染器只接收最终的完整命令字符串，并按现有约定以 shell 命令
   执行，不从字符串推导额外参数。

## 校验与错误

- Model 版本不是 `ready`、制品元数据不完整或租户不匹配时，创建直接失败。
- 创建时引擎类型、镜像和完整命令最终必须由调用方提供且非空；缺失时返回
  `InvalidArgument`。
- 用户传入的制品路径或校验和与 Model 不一致时拒绝创建。
- 引擎命令按字节保存，不做 shell 解析、参数合并或自动追加 `--served-model-name`。

## 测试范围

- Model 的 Get/List 版本响应包含三个引擎字段，供前端回填。
- 请求提供临时引擎值时，Gov 和 Inference 原样保存请求值。
- 请求缺少引擎值时，在入口校验阶段拒绝，不让 Inference 猜测启动配置。
- 命令字符串包含引号、空格和 `--served-model-name` 时保持原样。
- Model 版本未就绪或制品不匹配时不会写入 Inference 资源。

## 范围边界

本次不修改 Gov 仓库，不引入 IAM、GPU 调度或新的存储协议；GPU 资源仍由创建请求
中的 `resource.gpu` 单独传递。Model 的字段继续作为前端默认值来源，Inference 的
创建请求继续允许临时覆盖。
