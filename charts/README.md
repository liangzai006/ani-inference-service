# Charts

全新环境只看这个目录。这里不记录集群地址、节点 IP 或已有 Release。

| 路径 | 归属 | 会创建 | 不会创建 |
| --- | --- | --- | --- |
| `ani-inference` | 本仓库 | 推理 Deployment、Service、RBAC、数据库迁移 Job、普通 gRPC 服务端和 Model 客户端 `Certificate` | cert-manager、证书签发者、PostgreSQL、Ceph/S3、Model |
| `ani-inference-config` | 本仓库 | 空的 `LLMInferenceServiceConfig` 占位 | 引擎 command/args |
| `platform-values` | 上游安装参数 | 无。只是 LWS、KServe、LLMI 的 values | 任何集群资源 |
| `vendor` | 上游发布包 | KServe、LLMI、LWS、Higress、Gateway API（cert-manager 仅作版本参考） | ANI Chart 不安装 cert-manager |

`cert-manager`、`ClusterIssuer/ani-internal`、PostgreSQL、Ceph/S3、Model Service 是公共前提，必须由平台基础设施提前安装并保持就绪。ANI Chart 只向已有签发者申请服务端 `ani-inference-server-tls` 和 Model 客户端 `inference-model-mtls`，不会创建签发者或接管 cert-manager。

正式安装入口是 `docs/deploy/helm/ani-inference.md` 中的 Helm 和
`kubectl apply` 命令。推理服务只有一份 `charts/ani-inference/values.yaml`；网关地址用
`HIGRESS_PUBLIC_BASE_URL` 传入，不写进 values。仓库脚本只用于测试和 CI。
