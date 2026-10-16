# 平台依赖包

本目录保存安装脚本使用的上游 Helm Chart 和 CRD 清单。文件来自官方发布
包，未做内容修改，并随仓库保留，便于在新的环境中重复安装。每个文件的
SHA-256 校验值记录在 [`SHA256SUMS`](SHA256SUMS) 中。

## 来源

| 文件 | 官方来源 |
| --- | --- |
| KServe、LLMInferenceService | [KServe v0.16.0 发布页](https://github.com/kserve/kserve/releases/tag/v0.16.0) |
| cert-manager | 外部基础设施；不随本目录打包，由平台提前安装 |
| LeaderWorkerSet | [LWS v0.6.2 发布页](https://github.com/kubernetes-sigs/lws/releases/tag/v0.6.2) |
| Higress | [Higress Helm Chart 仓库](https://higress.io/helm-charts) |
| Gateway API | [Gateway API v1.6.0 发布页](https://github.com/kubernetes-sigs/gateway-api/releases/tag/v1.6.0) |
| Gateway API Inference Extension | [GIE v0.3.0 发布页](https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/tag/v0.3.0) |

安装编排由仓库根目录的 [`scripts`](../../scripts) 负责；请通过脚本读取这些
固定版本和校验值，不要直接修改或重新打包本目录的上游文件。容器镜像仓库、
镜像重写和私有镜像配置属于部署配置，由安装脚本或 Helm values 另行处理，
不包含在这些 Chart 包中。
