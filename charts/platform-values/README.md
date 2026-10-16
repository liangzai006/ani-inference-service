# 上游 Chart values

这些文件不是 ANI Chart，也不创建证书签发者。正式部署时由部署文档中的 Helm 命令传给
`charts/vendor/` 里的 LWS、KServe、LLMI Chart。cert-manager 是外部基础设施，必须在
正式部署前单独安装并保持就绪；`scripts/install-dependencies.sh` 只用于测试和 CI。

随仓库的 `cert-manager.yaml` 仅保留为平台镜像参考，不由 ANI 正式安装流程使用。

`cert-manager.yaml` 将控制器镜像指向新环境内部镜像仓库；如果仓库路径不同，
通过 `CERT_MANAGER_VALUES` 指定覆盖文件，并在安装前同步五个 cert-manager 镜像。
