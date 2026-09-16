# Inference 领域词汇

本文件定义独立 `ani-inference-service` 仓库的领域词汇；当前仓库已进入分阶段实现，仍不是 ANI 现有接口的新版本。入口见 [START-HERE](docs/START-HERE.md)。职责、状态和验收规则只在规格中维护。

| 词汇 | 含义及关系 |
|---|---|
| 推理服务 / InferenceService | 一份可独立部署、停止、恢复和发布的模型运行配置；不是模型目录条目 |
| 模型版本引用 / ModelArtifactRef | 指向 Model 领域某个不可变版本及制品摘要，不等同于模型名称或推理服务 ID |
| 引擎配置 / EngineSpec | 镜像、启动命令、模型装载路径、推理协议等运行配置；本阶段不另建引擎管理服务 |
| 资源要求 / ResourceRequirements | 遵循 Kubernetes requests/limits 的资源映射；GPU 使用 `nvidia.com/gpu` 等扩展资源名，不传 GPU 不注入 GPU，也不强制 CPU 参数 |
| 期望 / Desired | 用户已受理的运行目标及版本号 generation |
| 已应用 / Applied | Controller 已确认应用到运行资源的配置版本，不代表模型可调用 |
| Operation | 一次持久业务操作及其执行结果；不是资源当前健康状态 |
| RuntimeReady | 当前引擎进程和指定模型是否已完成加载、可提供约定协议 |
| Publication | 该推理服务面向数据面的发布意图、实际生效版本及撤销状态 |
| InvocationHealth | 最近一次调用探测结果及其时效；不是发布对象创建成功的同义词 |
| ResourceWork | 数据库中尚需处理的资源工作和下次观察义务，不是另一套业务资源状态机 |
| Controller | controller-runtime 的事件处理器；负责调用领域用例收敛，不继承 ANI 原有 Observer/Worker |
| 资源租户 / Actor / Caller Workload | 分别是资源归属方、行为主体、当前跳点的直接调用进程身份 |
| 控制面 / 数据面 | gRPC 管部署生命周期；数据面承载用户 Chat、Embedding、流式响应等请求 |
