# 上游 Chart values

这些文件不是 ANI Chart，也不创建证书签发者。正式部署时由部署文档中的 Helm 命令传给
`charts/vendor/` 里的 LWS、KServe、LLMI 和 cert-manager Chart。`scripts/install-dependencies.sh`
只用于测试和 CI。

正式 Helm 命令在空的新集群中使用固定版本和 `crds.enabled=true` 安装 cert-manager，
已有完整安装直接复用。随仓库的 `cert-manager.yaml` 用于内部镜像仓库配置。

`cert-manager.yaml` 将控制器镜像指向新环境内部镜像仓库；如果仓库路径不同，
通过 `CERT_MANAGER_VALUES` 指定覆盖文件，并在安装前同步五个 cert-manager 镜像。
