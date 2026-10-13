# 扁平化推理创建接口设计

## 目标

让业务前端只提交创建推理所需要的业务参数，不直接暴露 KServe、Kubernetes、制品 provider、GPU 调度计划和配额附件等内部结构。

模型版本提供默认的运行配置。前端选择模型版本后自动带出镜像、引擎类型和完整启动命令，用户可以在提交前临时修改；创建请求中的最终值优先于模型版本默认值。

## 公开创建参数

Gov 对外的创建请求使用扁平字段：

```json
{
  "idempotency_key": "req-001",
  "name": "qwen-inference",
  "model_version_id": "version-id",
  "served_model_name": "qwen2.5-1.5b-instruct",
  "engine_image": "registry.example/vllm:latest",
  "engine_type": "vllm",
  "command": "exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct",
  "cpu": "8",
  "memory": "32Gi",
  "replicas": 1,
  "gpu": null
}
```

多机多卡只在需要时增加：

```json
{
  "runtime_mode": "leader_worker_set",
  "worker_replicas": 1
}
```

GPU 选择只暴露业务选择所需的字段：

```json
{
  "cluster_id": "cluster-id",
  "pool_id": "pool-id",
  "profile_id": "profile-id",
  "profile_version": 1
}
```

GPU 的容器名、GPU Pod 数、每 Pod 设备数、Volcano/HAMI 参数和解析后的调度计划由 Gov/Accelerator/Inference 内部生成。没有 `gpu` 就不进入 GPU 配额路径。

## 模型版本默认值和临时覆盖

Model 版本保存：

- `engine_image`
- `engine_type`
- 完整 `command` 字符串

现有 `startup_command`/`startup_args` 字段保留兼容读取；新接口以完整 `command` 为业务字段，不要求前端组装 `command[]` 和 `args[]`。

前端选择 `model_version_id` 后读取这些默认值并填充表单。创建时必须以请求携带的最终值为准，Gov 不重新覆盖请求中的镜像或启动命令。Inference 保存最终配置快照，因此后续模型版本变化不会改变已经创建的推理服务。

## 启动命令执行

公开接口接受完整命令字符串，例如：

```text
exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct
```

Kubernetes/KServe 的容器规范仍然使用数组字段，但只用于固定的 shell 包装：

```yaml
command: ["/bin/sh", "-c"]
args:
  - exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct
```

服务不解析命令、不拼接参数、不注入默认引擎参数，也不把整条命令拆成 `command[]`/`args[]`。推理镜像必须包含 `/bin/sh`；调用方负责提供可执行的 shell 命令，并建议使用 `exec` 让引擎正确接收终止信号。

## 资源规则

- `cpu` 和 `memory` 是前端的资源请求输入，转换为 Kubernetes requests/limits 的业务资源。
- `replicas` 表示推理副本或 LWS 组数。
- `runtime_mode` 缺省为 KServe Deployment；只有分布式部署时才传 `leader_worker_set`。
- `worker_replicas` 只有 LWS 才有效。
- GPU 请求只选择集群、GPU 池和 GPU profile；实际 GPU Pod 数由部署模式和副本数推导。

## 调用链

```text
前端选择模型版本并编辑最终运行配置
  -> Gov 接收扁平请求
  -> Gov 查询 Model 确认版本 Ready
  -> 有 GPU 时执行 Accelerator 配额受理
  -> Gov 将请求中的最终镜像和 command 原样转给 Inference
  -> Inference 持久化配置并创建 KServe 资源
  -> KServe 启动容器
  -> Higress 按 served_model_name 路由推理请求
```

Gov 不向启动命令追加参数，Inference 不从 Model 覆盖请求值，KServe 只负责将命令交给容器执行。

## 兼容和迁移

1. 在 Model 版本契约中增加 `engine_image`，并增加完整 command 的读取/写入路径。
2. 在 Gov 增加扁平创建推理 DTO；旧的嵌套 `engine`/`runtime`/`resource` 请求保留一版兼容，但标记为旧接口。
3. Gov 内部继续组装 Inference 的内部请求，外部请求不携带 KServe provider、artifact provider 或 GPU 调度附件。
4. Inference 内部可以继续使用 Kubernetes 的数组字段完成容器对象渲染，但不得向 command 字符串添加任何内容。
5. 更新请求未提供运行配置时保留当前 generation；提供时以请求中的完整配置替换，并记录新的配置快照。

## 验证范围

- Model 版本返回默认镜像和完整 command。
- 前端修改 command 后，Gov 转发值与请求完全一致。
- 无 GPU 请求不会调用 Accelerator 配额路径。
- GPU 请求只由 cluster/pool/profile 选择生成内部 GPU 计划。
- KServe 对象使用固定 `/bin/sh -c` 包装，并将请求字符串作为唯一命令参数。
- CPU、内存、副本数和 LWS worker 数按请求渲染。
- 旧接口和旧模型版本仍可读取，但新接口不自动拼接旧的 `startup_args`。
