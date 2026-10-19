# Higress 原生 API 路由迁移验证记录

日期：2026-10-19

## 已完成

- `HTTPRoutePublisher` 的发布、撤销和确认逻辑已接入 Higress Console 原生 API。
- 每个推理服务使用确定性的 Service Source 记录 KServe predictor Service 的集群 DNS；模型路由统一使用 `/v1/` 和 `x-higress-llm-model` 精确匹配，并直接指向该 predictor DNS。
- Chart 删除模型 HTTPRoute 模板，保留无 selector 的 sink Service；Inference 进程启动时创建或更新 bootstrap 路由和 Service Source。
- Console 账号通过 Kubernetes Secret 的 `username`、`password` 键注入，未写入 values、镜像或日志。
- 删除推理服务时先删除原生 route，再删除 Service Source；限流插件的 owner 在 route 确认消失后清理。
- 运行时 Role 已删除 Gateway API HTTPRoute 权限，因为业务路由不再由 Kubernetes Gateway API 管理。

## 集群验证补充

在目标集群用已有的 `tttt` 推理服务验证了两种原生路由后端：

- 将 `services[].name` 设置为 Service Source 名称时，Higress 返回 503，Envoy 没有向
  predictor Service 发出请求；
- 将 `services[].name` 设置为
  `tttt-predictor.ani-system.svc.cluster.local` 后，携带 API key 调用
  `/v1/completions` 返回 HTTP 200，响应模型为 `qwen3.5-0.8b`，且返回了非空文本。

因此最终实现保留 Service Source 作为 Higress 管理元数据和 bootstrap 依赖，但模型
路由的实际后端必须是 KServe predictor Service DNS。此前遗留的 Gateway API HTTPRoute
会与原生路由冲突并造成 503，已在验证集群删除由 ANI 管理的旧 HTTPRoute。

## Governance 完整链路验证

使用验证租户通过 Governance NodePort `192.168.102.68:32248` 完成了一次新建、调用和删除：

- 创建服务 `gov-higress-e2e-tag-20261019`，资源 ID 为
  `0935dd18-9d86-4251-8f90-067d9058cc33`，创建操作为
  `252c3b2b-120d-4ec4-891e-619b1e9d4c10`；请求携带完整 CPU vLLM 命令和可拉取的
  `docker.changqingyun.cn/mirror/vllm-openai-cpu:latest` 镜像标签。
- Governance 创建操作最终为 `succeeded`，服务为 `ready`，KServe Predictor 为
  `1/1 Running`，Higress Console 自动出现带模型 Header 的原生 route。
- 经 Higress NodePort `192.168.102.68:30090/v1/completions` 携带 API key 调用，
  返回 HTTP 200，模型为 `qwen3.5-0.8b-gov-e2e-tag-20261019`，返回非空文本。
- 通过 Governance 删除操作 `a724ef93-36fb-4422-8f9d-d1b280b7ce21`，最终为
  `succeeded`；Inference 记录为 `stopped/withdrawn`，KServe 资源、PVC、模型 route
  和 Service Source 均已清理。

本次服务详情中的 `health` 仍为 `unknown`，但 KServe readiness 和实际 Higress 请求
均成功；这仍是观测状态回写延迟/语义不一致问题，不能作为本次链路失败依据，但需要单独修复。

## 本地验证

- `go test ./internal/data/kubernetes ./cmd/ani-inference-service`：通过。
- `go test ./...`：通过。
- `helm lint charts/ani-inference`：通过，仅有 Chart 缺少 icon 的提示。
- `helm template`：通过；输出包含 sink Service 和 Console Secret 引用，不包含模型 HTTPRoute 或明文密码。
- 缺少 `higress.consoleURL` 或 Console Secret 名称时，Helm 渲染按预期失败。

## 集群验证前置条件

需要使用目标集群已有的 Higress Console 地址、账号 Secret、Inference 数据库/证书配置，并通过 Helm 安装更新后的 Inference Chart。随后检查：

1. Console `/v1/routes` 能看到 bootstrap 路由和模型路由。
2. `POST /v1/completions` 只通过 Higress AI 入口访问，并返回目标模型响应。
3. 删除推理服务后，原生 route 和 Service Source 均返回 404。
4. 同名重建不会复用旧的 route 或 Service Source。

治理业务接口 `/api/v1/...` 仍由 Governance BFF 提供，不配置到 Higress AI 入口。
