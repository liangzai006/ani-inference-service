# Inference Helm 部署

Inference 的 Helm Chart 和平台依赖安装脚本位于本仓库。Model Chart 位于
`ani-model-service` 仓库；ANI 业务服务安装到新环境的 `ani-system`，KServe/LWS
和 Higress 使用各自的控制面命名空间。

新环境的正式入口是 [Inference Helm 部署文档](ani-inference.md) 中的 Helm 和
`kubectl apply` 命令。Helm 使用当前 kubeconfig context，不传
`--kube-context`；安装前应显式核对当前 context。PostgreSQL、Ceph/S3、
ClusterIssuer 和 Model Service 仍按外部公共流程提供。

`scripts/install.sh`、`scripts/install-dependencies.sh` 和 `scripts/preflight.sh`
只用于测试、离线演练和 CI，不作为正式安装入口。

- [完整安装流程](ani-inference.md)
- [Inference Chart 说明](../../../charts/README.md)
- [依赖下载脚本](../../../scripts/fetch-dependencies.sh)
- [安装后只读预检](../../../scripts/preflight.sh)
