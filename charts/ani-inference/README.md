# ani-inference

推理控制面 Chart，安装到 `ani-system`。一份 values：`values.yaml`。

会创建：

- Deployment、gRPC Service、admin Service、ServiceAccount、Role、RoleBinding
- 安装前的数据库迁移 Job
- `Certificate/inference-model-mtls`

不会创建：

- cert-manager（安装脚本在空的新集群中补齐，已有就绪安装会复用）
- `ClusterIssuer` 或其他签发者
- PostgreSQL、Ceph/S3、Model Service

`Certificate` 引用已经存在的 `ClusterIssuer/ani-internal`。签发结果写入 Secret `inference-model-mtls`，客户端 DNS SAN 只有 `ani-inference.ani.internal`。进程用它访问 Model，并校验对端身份 `ani-model.ani.internal`。

数据库只读取已有 Secret `inference-database` 的 `dsn`。`higress.publicBaseURL` 必须在安装时传入。

不要单独安装这个 Chart。先按 `docs/deploy/helm/ani-inference.md` 中的 Helm 命令装好
Gateway API、Higress、LWS 和 KServe，再用 Helm 安装本 Chart。仓库安装脚本只用于测试和 CI。
