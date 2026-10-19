# ani-inference

推理控制面 Chart，安装到 `ani-system`。一份 values：`values.yaml`。

会创建：

- Deployment、gRPC Service、admin Service、控制面 ServiceAccount、runtime ServiceAccount、Role、RoleBinding
- 安装前的数据库迁移 Job
- `Certificate/ani-inference-server-tls`（普通业务 gRPC 服务端证书）
- `Certificate/inference-model-mtls`（Inference 调用 Model 的客户端证书）
- 一个没有 selector 的 sink Service。Inference 启动时通过 Higress Console 原生 API
  创建 bootstrap 路由和 Service Source，让 `model-router` 先读取请求体的 `model`
  字段并触发重算路由；未知模型不会落到其他模型。bootstrap 和模型路由统一匹配
  `/v1/`，所以 `/v1/completions`、`/v1/chat/completions` 等 OpenAI 兼容接口无需
  分别配置。每个模型路由实际指向对应的 KServe predictor Service DNS，Service Source
  仅作为 Higress 管理元数据和 bootstrap 依赖保留。

`imagePullSecrets` 会用于控制面 Deployment，并挂到独立的 runtime ServiceAccount。
物化 Job、KServe
`InferenceService` 和 `LLMInferenceService` 使用这个账号拉取模型引擎及物化镜像，
控制面 ServiceAccount 不会被运行时 Pod 复用。

不会创建：

- cert-manager（外部基础设施，必须预先安装并保持就绪）
- `ClusterIssuer` 或其他签发者
- PostgreSQL、Ceph/S3、Model Service

`Certificate` 引用已经存在的 `ClusterIssuer/ani-internal`。Inference gRPC 唯一监听器始终使用 TLS 1.3 和双向证书校验，服务端证书写入 `ani-inference-server-tls`，DNS SAN 为 `ani-inference.ani.internal`；Governance 和 Model 的客户端证书必须由同一 CA 签发，并通过 RPC 身份策略。Governance 客户端唯一 DNS SAN 为 `ani-governance`，查询、CPU 生命周期和 GPU 合同请求都走 `19090`。Inference 调用 Model 的客户端证书写入 `inference-model-mtls`，其 DNS SAN 为 `ani-inference.ani.internal`，并校验对端身份 `ani-model.ani.internal`。

数据库只读取已有 Secret `inference-database` 的 `dsn`。`higress.publicBaseURL`、
`higress.consoleURL` 和 `higress.consoleCredentialsSecret.name` 必须在安装时传入。
Console 凭据只从该 Secret 的 username/password 键注入控制面，不写入 values 或日志。

默认会在 `higress.rateLimitNamespace` 创建一个 Role 和 RoleBinding，将控制面
ServiceAccount 绑定到仅能 `get`/`patch` 指定 `higress.rateLimitPlugin` 的权限。
如果限流插件在其他命名空间，可同时设置 `higress.rateLimitNamespace` 和
`higress.rbac.namespace` 为该命名空间。

模型发布前，控制面会把 `served-model` 写入限流插件，再通过 Higress 原生 API 创建路由；最后一条
同模型路由撤回后会清理由 Inference 创建的限流项。每个模型默认每秒 10 次，插件
配置中的其他模型项会保留。

不要单独安装这个 Chart。先按 `docs/deploy/helm/ani-inference.md` 中的 Helm 命令装好
Gateway API、Higress、LWS 和 KServe，再用 Helm 安装本 Chart。仓库安装脚本只用于测试和 CI。
