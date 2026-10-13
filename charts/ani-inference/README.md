# ani-inference

推理控制面 Chart，安装到 `ani-system`。一份 values：`values.yaml`。

会创建：

- Deployment、gRPC Service、admin Service、ServiceAccount、Role、RoleBinding
- 安装前的数据库迁移 Job
- `Certificate/ani-inference-server-tls`（普通业务 gRPC 服务端证书）
- `Certificate/inference-model-mtls`（Inference 调用 Model 的客户端证书）
- 一个没有后端的 Higress `HTTPRoute` 兜底规则。它让 `model-router` 先读取
  请求体的 `model` 字段并触发重算路由；未知模型会由网关拒绝，不会落到其他模型。

不会创建：

- cert-manager（安装脚本在空的新集群中补齐，已有就绪安装会复用）
- `ClusterIssuer` 或其他签发者
- PostgreSQL、Ceph/S3、Model Service

`Certificate` 引用已经存在的 `ClusterIssuer/ani-internal`。普通 Inference gRPC 监听器始终使用 TLS 1.3 和双向证书校验，服务端证书写入 `ani-inference-server-tls`，DNS SAN 为 `ani-inference.ani.internal`；Governance 和 Model 的客户端证书必须由同一 CA 签发，并通过 RPC 身份策略。Inference 调用 Model 的客户端证书写入 `inference-model-mtls`，其 DNS SAN 为 `ani-inference.ani.internal`，并校验对端身份 `ani-model.ani.internal`。

数据库只读取已有 Secret `inference-database` 的 `dsn`。`higress.publicBaseURL` 必须在安装时传入。

不要单独安装这个 Chart。先按 `docs/deploy/helm/ani-inference.md` 中的 Helm 命令装好
Gateway API、Higress、LWS 和 KServe，再用 Helm 安装本 Chart。仓库安装脚本只用于测试和 CI。
