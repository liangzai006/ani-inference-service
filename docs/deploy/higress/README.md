# 新环境 Higress 推理网关

本目录提供 Higress 推理网关的部署配置。当前模型推理的 HTTP 入口统一使用
Higress；安装时通过下文的变量指定目标新环境。

Higress 版本为 2.2.4，启用 Gateway API。网关通过 NodePort `30090`（HTTPS
为 `30443`）暴露，使用 `higress` GatewayClass。内置可观测性组件把网关
指标和日志写入新集群 CephFS 提供的 PVC。

model-router 插件配置为 `enableOnPathSuffix: ["*"]`，会从 OpenAI 兼容 JSON 请求中读取
`model`，再写入 `x-higress-llm-model` 请求头。Inference Chart 会安装一条指向无 selector sink Service 的
`/v1/` 兜底路由，让插件有机会先执行并重新计算路由；ANI 随后为每个模型发布一条
`/v1/` 模型路由，因此 `/v1/completions`、`/v1/chat/completions`、`/v1/embeddings`
等带 `model` 的 API 都复用同一条模型路由。未知模型不会落到任意一个已部署模型，当前
由 sink Service 返回不可用错误。调用方只传 JSON `model`，不应手工添加内部模型路由头。

## 推理网关 API Key

动态 Inference-owned AK 的网关链路使用 `ani-inference-ext-auth`：Higress 的官方
`ext-auth` WasmPlugin 把 `x-api-key`、model-router 生成的模型头和请求 ID 转发给
`ani-inference-authz`，adapter 再用 `ani-inference-gateway.ani.internal` 的 mTLS
身份调用 Inference `CheckInferenceAccess`。当前网关按 key 和 model-router 从请求体提取的 served model 做校验。Inference
在持久发布记录中解析实际服务和租户，再同时检查 Key 的服务、模型范围及服务访问策略；
不信任调用方提供的服务 ID 或租户头。授权失败按 401/403/429 返回，Inference
或 adapter 不可用时按 503 fail-close。先把 Chart 的 `gatewayAuthz.enabled` 打开并
发布含 `/ani-inference-gateway-authz` 的镜像，再应用独立的 `ext-auth.yaml`
对象；该插件只对 `/v1/` 前缀和 `/v1` 精确路径生效，`/api/v1/` 仍由 Governance BFF 处理；没有
adapter 时不要应用这个插件。

已有 release 如包含额外的环境变量、挂载或证书配置，先保存现场 Deployment 并审查
增量部署。记录 `gatewayAuthz.enabled`、主服务与 companion 镜像、证书 issuer 覆盖项；
后续 Helm 升级须将这些覆盖项与现有现场配置一起合入 values，经渲染比对后应用，
避免旧 release values 覆盖已验证的运行配置。不要仅用基础 Chart 重建现场 Deployment。


动态认证的启用顺序（使用已确认的 `KUBE_CONTEXT`）：

```bash
# 先部署并确认 Inference、gateway-authz companion 和客户端 Certificate Ready。
# 官方插件 OCI 镜像必须先镜像到内部仓库；ext-auth.yaml 固定官方 2.0.3 manifest digest。
KUBE_CONTEXT="$KUBE_CONTEXT" bash docs/deploy/higress/register-authz.sh
kubectl --context "$KUBE_CONTEXT" apply --dry-run=server -f docs/deploy/higress/ext-auth.yaml
kubectl --context "$KUBE_CONTEXT" apply -f docs/deploy/higress/ext-auth.yaml
```

`register-authz.sh` 仅向已有 `higress-system/default` McpBridge 追加私有 DNS 服务来源，
保留其他条目并用 resourceVersion 防止并发覆盖，不创建公网 adapter 路由。
Higress 插件使用这个来源的 `ani-inference-authz.dns:8080`，仅有 Kubernetes Service
不足以保证 Envoy 已发现该 cluster。`match_type`/`match_list` 属于
`spec.defaultConfig` 中的插件配置，不能写到 WasmPlugin 的 `spec` 顶层。

通过 Envoy config dump 确认 ext-auth Wasm 已加载、authz cluster healthy 后，再移除迁移期
`ani-inference-key-auth` WasmPlugin；通过 Gateway API bootstrap 路由接管时，移除同名旧
`higress-system/ani-inference-model-router` Ingress。提前保存旧对象作为回滚输入。
必须验证动态 key 通过、无效/撤销 key 401、模型 scope 403、key 限流 429、依赖失败 503，
并独立验证 Governance 管理入口与 Higress 管理路径 404。公共 `plugins.yaml` 不包含
ext-auth，避免基础安装在 companion 尚未部署时启用动态认证。

adapter 镜像由仓库根目录的 `Dockerfile.authz` 构建：先编译
`.build/ani-inference-gateway-authz`，再构建并推送
`docker.changqingyun.cn/ani/inference-gateway-authz:<tag>`。Inference 主镜像仍由
`Dockerfile` 构建，两者版本应保持同一源码提交。

仅使用迁移期静态 bootstrap 模式时应用 `key-auth.yaml.tmpl`，推理网关要求请求携带 `x-api-key`。真实密钥
不会写入 Git 或主插件清单。请在新环境的受保护终端中设置 context、网关节点
地址和密钥，再渲染并应用模板：

```bash
export KUBE_CONTEXT="${KUBE_CONTEXT:?请填写新环境 context}"
test "$(kubectl config current-context)" = "$KUBE_CONTEXT" || {
  echo "当前 kubeconfig context 不是目标新集群" >&2
  exit 1
}
export GATEWAY_NODE_IP="${GATEWAY_NODE_IP:?请设置新环境网关节点 IP}"
export HIGRESS_PUBLIC_BASE_URL="http://${GATEWAY_NODE_IP}:30090"
export ANI_HIGRESS_API_KEY="${ANI_HIGRESS_API_KEY:?请从新环境受保护凭据读取 API Key}"
sed "s|\${ANI_HIGRESS_API_KEY}|${ANI_HIGRESS_API_KEY}|g" \
  docs/deploy/higress/key-auth.yaml.tmpl |
  kubectl apply -f -
```

调用方把密钥放在请求头中：

```bash
curl -sS -X POST "${HIGRESS_PUBLIC_BASE_URL}/v1/completions" \
  -H "content-type: application/json" \
  -H "x-api-key: ${ANI_HIGRESS_API_KEY}" \
  -d '{"model":"<served-model-name>","prompt":"hello","max_tokens":16}'
```

当前网关只有推理 listener，因此认证对该 listener 全局生效。以后如果增加
其他公共路由，应在暴露前改为按路由认证。使用新密钥重新应用模板会轮换密钥，
旧密钥随即失效。

这里的模板仍是迁移期静态 bootstrap key，不是 Inference-owned 的多租户 AK
签发模块。动态链路启用后，不要同时应用静态 `key-auth`，否则会把静态 consumer
和 Inference-owned key 叠加成两套入口策略。动态 key 的签发、撤销、租户/模型范围
和按 key 限流按[`docs/design/ai-gateway-auth-and-rate-limit.md`](../../design/ai-gateway-auth-and-rate-limit.md)
分阶段落地。

## 镜像

安装前把 Chart 使用的镜像和 Wasm 插件镜像复制到新环境可访问的镜像仓库。
源镜像如下：

```text
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/higress:2.2.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/pilot:2.2.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/gateway:2.2.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/console:2.2.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/grafana:9.3.6
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/prometheus:v2.40.7
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/loki:2.9.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/promtail:2.9.4
higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/model-router:2.0.2
higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ai-statistics:2.0.2
higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/key-rate-limit:1.0.0
higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/key-auth:2.0.0
higress-registry.cn-hangzhou.cr.aliyuncs.com/plugins/ext-auth:2.0.3
```

部署 values 和插件清单已经指向对应的
`docker.changqingyun.cn/mirror` 地址。

## 安装

完整新环境安装请按上级
[`ani-inference.md`](../helm/ani-inference.md) 中的 Helm 和 `kubectl apply` 命令；
下面的命令用于单独维护 Higress 组件，不需要在完整安装后重复执行。

以下命令使用当前 kubeconfig context。执行前先确认它是 `.clusterenv` 对应的新集群：

```bash
export KUBE_CONTEXT="${KUBE_CONTEXT:?请填写新环境 context}"
test "$(kubectl config current-context)" = "$KUBE_CONTEXT" || {
  echo "当前 kubeconfig context 不是目标新集群" >&2
  exit 1
}
helm upgrade --install higress charts/vendor/higress-2.2.4.tgz \
  --namespace higress-system --create-namespace \
  --values docs/deploy/higress/new-cluster-values.yaml \
  --wait --timeout 15m
kubectl apply -f docs/deploy/higress/gateway.yaml
kubectl apply -f docs/deploy/higress/plugins.yaml
```

随后按 [Inference Helm 部署文档](../helm/ani-inference.md) 部署或更新新环境 Inference 服务。最后按上面的 API Key
步骤应用 `key-auth.yaml.tmpl`，避免入口在认证前被公开。Console 可以继续作为运维 UI
部署，但 Inference 不再调用其 REST API；后续可单独移除 Console Pod。

## 验证

发送流量前先检查网关和路由状态：

```bash
kubectl -n higress-system get pods,svc,gateway
kubectl get gatewayclass higress -o yaml
kubectl -n higress-system get gateway ani-higress -o yaml
kubectl -n higress-system get wasmplugin
kubectl -n ani-system get httproute -o yaml
```

所有模型和 OpenAI 兼容 API 都使用同一个入口地址：

```bash
export GATEWAY_NODE_IP="${GATEWAY_NODE_IP:?请设置新环境网关节点 IP}"
export HIGRESS_PUBLIC_BASE_URL="${HIGRESS_PUBLIC_BASE_URL:-http://${GATEWAY_NODE_IP}:30090}"
curl -sS -X POST "${HIGRESS_PUBLIC_BASE_URL}/v1/completions" \
  -H 'content-type: application/json' \
  -H "x-api-key: ${ANI_HIGRESS_API_KEY}" \
  -d '{"model":"<served-model-name>","prompt":"hello","max_tokens":16}'

curl -sS -X POST "${HIGRESS_PUBLIC_BASE_URL}/v1/chat/completions" \
  -H 'content-type: application/json' \
  -H "x-api-key: ${ANI_HIGRESS_API_KEY}" \
  -d '{"model":"<served-model-name>","messages":[{"role":"user","content":"hello"}],"max_tokens":16}'
```

可重复的冒烟检查脚本是 `verify-vllm.sh`。运行前设置 `ANI_TEST_MODEL`、
`ANI_HIGRESS_API_KEY`，并设置 `ANI_HIGRESS_URL="$HIGRESS_PUBLIC_BASE_URL"`
或 `GATEWAY_NODE_IP`。

应验证两个不同模型、未知模型、流式请求、网关指标/AI 统计，以及超过配置
限制后的 HTTP 429。当前未接入 IAM，限流按 served model 计算，每个模型为
每秒 10 个请求；这是每个 Higress 数据面实例的本地限流，不是跨网关副本的
全局总量。Inference 发布器会根据有效 ANI HTTPRoute 同步
`higress-system/ani-request-rate-limit`：发布模型后先加入限流配置，再暴露
路由；最后一条路由撤回后移除由 Inference 创建的模型配置，同时保留其他模型
和人工配置。Higress 要求 `limit_keys` 至少有一条有效项，因此没有其他项时会
保留最后一条无主配置，不能用空数组关闭插件。Token 配额和按身份限流暂不接入。

Inference Helm Chart 默认创建跨命名空间的最小 Role/RoleBinding。控制面
ServiceAccount 只能读取和修改指定的 `ani-request-rate-limit` WasmPlugin；如果
手工关闭 `higress.rbac.create`，模型发布会因没有限流配置权限而失败。

## 回滚

保持 Higress values 和清单的版本记录。仅回滚网关时执行：

```bash
helm rollback higress <revision> \
  --namespace higress-system
kubectl apply -f docs/deploy/higress/gateway.yaml
kubectl apply -f docs/deploy/higress/plugins.yaml
```

Inference Deployment 继续使用 `ANI_HIGRESS_*` 变量。此操作与旧环境相互
独立，不会触碰旧环境资源。
