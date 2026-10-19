# Higress 原生 API 路由迁移验证记录

日期：2026-10-19

## 已完成

- `HTTPRoutePublisher` 的发布、撤销和确认逻辑已接入 Higress Console 原生 API。
- 每个推理服务使用确定性的 Service Source，指向 KServe predictor Service 的集群 DNS；模型路由使用 `/v1/completions` 和 `x-higress-llm-model` 精确匹配。
- Chart 删除模型 HTTPRoute 模板，保留无 selector 的 sink Service；Inference 进程启动时创建或更新 bootstrap 路由和 Service Source。
- Console 账号通过 Kubernetes Secret 的 `username`、`password` 键注入，未写入 values、镜像或日志。
- 删除推理服务时先删除原生 route，再删除 Service Source；限流插件的 owner 在 route 确认消失后清理。
- 运行时 Role 已删除 Gateway API HTTPRoute 权限，因为业务路由不再由 Kubernetes Gateway API 管理。

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
