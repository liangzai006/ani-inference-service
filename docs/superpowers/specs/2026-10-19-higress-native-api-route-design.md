# 使用 Higress 原生管理 API 发布推理路由

日期：2026-10-19

## 背景

当前 Inference 服务通过 Kubernetes Gateway API 创建 `HTTPRoute`。这些路由已经由
Higress Controller 接收并转发流量，但 Higress Console 的路由管理页面不一定展示
`ani-system` 中由 ANI 动态创建的 `HTTPRoute`。

本次改造的目标是让推理服务的路由由 Higress Console 原生管理 API 创建，模型路由
可以在 Higress Console 的路由管理中看到，同时继续由 ANI 随推理服务的创建、发布和
删除维护路由生命周期。

## 范围和边界

- 只改 AI 推理流量的网关配置。
- AI 请求继续从 Higress 进入，例如 `/v1/completions`。
- Governance 业务接口 `/api/v1/...` 继续走 Governance BFF，不接入 Higress。
- 不修改模型导入、配额、IAM 或推理业务 API 契约。
- 不保留两套模型路由作为长期运行方案，避免同一模型出现重复转发和删除竞态。

## 方案

### Higress API 客户端

Inference 增加一个独立的 Higress Console API 适配器，负责：

- 登录或建立带认证的 Console API 会话；
- 创建、更新、查询和删除路由；
- 创建、更新、查询和删除 Service Source；
- 将 404 删除视为幂等成功，将冲突转为可重试的状态；
- 不把 Console API 的请求模型泄漏到业务层。

使用的原生接口为：

- `POST/PUT/GET/DELETE /v1/routes`；
- `POST/PUT/GET/DELETE /v1/service-sources`；
- `/session/login` 或 Higress 版本实际启用的认证入口。

Higress Console API 的管理员凭据通过 Kubernetes Secret 注入，不写入 Helm values、
代码或日志。Console API 地址使用集群内地址，例如
`http://higress-console.higress-system.svc.cluster.local:8080`。

### Service Source

每个已发布的推理服务创建一个确定性 Service Source，指向其 KServe predictor
Service 的集群 DNS 名称和服务端口。例如：

```text
tttt-predictor.ani-system.svc.cluster.local:80
```

Service Source 名称包含推理服务 ID 和发布代次，避免删除后重建时复用旧配置。创建和
删除操作必须与路由发布状态绑定，不能在只写数据库记录时提前暴露路由。

### 模型路由

每个已发布推理服务创建一条原生路由：

- 路径前缀：当前保持 `/v1/completions`；
- Header 精确匹配：`x-higress-llm-model=<served_model_name>`；
- 后端：当前推理服务的 Service Source；
- 权重：100；
- 路由名：由租户、推理服务 ID 和 generation 确定性生成。

路由创建、更新、撤销和删除必须是幂等的，并在失败时返回明确的发布错误，不能把
数据库中的 `published` 状态提前写成成功。

### 初始匹配入口

Higress 的 model-router 插件需要先接收请求，从 JSON 的 `model` 字段设置
`x-higress-llm-model`，再重新计算模型路由。原先的 Gateway API 入口路由没有后端，
而 Higress 原生普通路由接口要求存在后端服务，因此原生 API 方案需要一个专用的
无 Endpoints sink Service 作为初始匹配后端：

- 已知模型由 model-router 重新计算到模型路由；
- 未知模型不会落到任意其他模型，而是返回不可用错误；
- sink Service 不承载业务流量，也不增加推理副本。

如果在目标 Higress 版本验证出原生 API 支持无后端路由，则取消 sink Service，使用
无后端入口；实现以集群实际 API 校验结果为准。

## 配置

Inference Helm values 增加以下配置，敏感值只通过 Secret 引用：

```yaml
higress:
  consoleURL: http://higress-console.higress-system.svc.cluster.local:8080
  consoleCredentialsSecret:
    name: ani-higress-console-credentials
    usernameKey: username
    passwordKey: password
```

现有 `publicBaseURL` 继续用于对外返回推理调用地址。Higress Gateway 的名称和命名空间
仍用于校验网关可用性，但不再用于创建模型 `HTTPRoute`。

## 失败和恢复

- 创建 Service Source 成功、路由创建失败：删除刚创建的 Service Source，并将发布操作
  标记失败；允许重试。
- 路由创建成功、数据库回写失败：下一次 reconcile 通过确定性名称查询并补齐状态。
- 删除路由遇到 404：视为已删除；删除 Service Source 也采用相同规则。
- Console API 认证失败、超时或 5xx：不标记为发布成功，保留可重试错误。
- Higress 原生资源和数据库状态不一致：以资源查询结果为依据进行收敛，禁止创建重复
  路由。

## 验证

### 单元和契约测试

- 登录、会话失效和重试；
- 路由和 Service Source JSON 字段映射；
- Header 精确匹配、路径前缀和后端服务端口；
- 创建、更新、删除幂等；
- 失败回滚和 404 删除；
- 不同租户、服务和 generation 的名称不冲突。

### 集群联调

1. 创建推理服务，确认路由出现在 Higress Console 的路由列表和
   `GET /v1/routes` 返回中。
2. 确认 Service Source 指向正确的 `ani-system` Service。
3. 使用 Higress NodePort、API key 和 `model` 请求 `/v1/completions`，确认返回模型
   响应。
4. 使用未知模型确认不会转发到其他模型。
5. 删除推理服务，确认路由和 Service Source 都被删除，再确认同名服务可重建。
6. 分别确认 Governance `/api/v1/...` 不经过 Higress。

## 不在本次范围

- 增加 `/v1/chat/completions`、`/v1/embeddings` 等新的 OpenAI 路径；
- 改造 Higress Console 前端页面；
- 把 Governance 业务 API 接入 Higress；
- 引入新的 IAM 或租户认证体系。
