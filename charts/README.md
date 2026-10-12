# Charts

全新环境只看这个目录。这里不记录集群地址、节点 IP 或已有 Release。

| 路径 | 归属 | 会创建 | 不会创建 |
| --- | --- | --- | --- |
| `ani-inference` | 本仓库 | 推理 Deployment、Service、RBAC、数据库迁移 Job、零信任客户端 `Certificate` | cert-manager、证书签发者、PostgreSQL、Ceph/S3、Model |
| `ani-inference-config` | 本仓库 | 空的 `LLMInferenceServiceConfig` 占位 | 引擎 command/args |
| `platform-values` | 上游安装参数 | 无。只是 LWS、KServe、LLMI 的 values | 任何集群资源 |
| `vendor` | 上游发布包 | KServe、LLMI、LWS、Higress、Gateway API、cert-manager | 无（正式 Helm 命令安装 cert-manager） |

`ClusterIssuer/ani-internal`、PostgreSQL、Ceph/S3、Model Service 是公共前提。安装脚本会在空的新集群中补齐 cert-manager，完整就绪时复用，部分安装则停止保护现有资源。`ani-inference` 只向已有签发者申请 `inference-model-mtls`，DNS SAN 只有 `ani-inference.ani.internal`。

正式安装入口是 `docs/deploy/helm/ani-inference.md` 中的 Helm 和
`kubectl apply` 命令。推理服务只有一份 `charts/ani-inference/values.yaml`；网关地址用
`HIGRESS_PUBLIC_BASE_URL` 传入，不写进 values。仓库脚本只用于测试和 CI。
