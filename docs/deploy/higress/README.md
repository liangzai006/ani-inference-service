# 新环境 Higress 推理网关

本目录提供 Higress 推理网关的部署配置。当前模型推理的 HTTP 入口统一使用
Higress；安装时通过下文的变量指定目标新环境。

Higress 版本为 2.2.4，启用 Gateway API。网关通过 NodePort `30090`（HTTPS
为 `30443`）暴露，使用 `higress` GatewayClass。内置可观测性组件把网关
指标和日志写入新集群 CephFS 提供的 PVC。

model-router 插件从路径以 `/completions` 结尾的 JSON 请求中读取 `model`，
再写入 `x-higress-llm-model` 请求头。Inference Chart 会安装一条没有后端的
`/v1/completions` 兜底 HTTPRoute，让插件有机会先执行并重新计算路由；ANI
随后为每个模型发布一条 HTTPRoute，使用该请求头精确匹配，并固定使用
`/v1/completions` 路径。未知模型不会落到任意一个已部署模型，当前由无后端
HTTPRoute 返回 500。调用方只传 JSON `model`，不应手工添加内部模型路由头。

## 推理网关 API Key

应用 `key-auth.yaml.tmpl` 后，推理网关要求请求携带 `x-api-key`。真实密钥
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
步骤应用 `key-auth.yaml.tmpl`，避免入口在认证前被公开。

## 验证

发送流量前先检查网关和路由状态：

```bash
kubectl -n higress-system get pods,svc,gateway
kubectl get gatewayclass higress -o yaml
kubectl -n higress-system get gateway ani-higress -o yaml
kubectl -n higress-system get wasmplugin
kubectl -n ani-system get httproute -o yaml
```

所有模型的验收请求都使用同一个入口地址：

```bash
export GATEWAY_NODE_IP="${GATEWAY_NODE_IP:?请设置新环境网关节点 IP}"
export HIGRESS_PUBLIC_BASE_URL="${HIGRESS_PUBLIC_BASE_URL:-http://${GATEWAY_NODE_IP}:30090}"
curl -sS -X POST "${HIGRESS_PUBLIC_BASE_URL}/v1/completions" \
  -H 'content-type: application/json' \
  -H "x-api-key: ${ANI_HIGRESS_API_KEY}" \
  -d '{"model":"<served-model-name>","prompt":"hello","max_tokens":16}'
```

可重复的冒烟检查脚本是 `verify-vllm.sh`。运行前设置 `ANI_TEST_MODEL`、
`ANI_HIGRESS_API_KEY`，并设置 `ANI_HIGRESS_URL="$HIGRESS_PUBLIC_BASE_URL"`
或 `GATEWAY_NODE_IP`。

应验证两个不同模型、未知模型、流式请求、网关指标/AI 统计，以及超过配置
限制后的 HTTP 429。当前未接入 IAM，限流按 served model 计算，每个模型为
每秒 10 个请求。Inference 发布器会根据有效 ANI HTTPRoute 同步
`higress-system/ani-request-rate-limit`：发布模型后先加入限流配置，再暴露
路由；最后一条路由撤回后移除模型配置。Token 配额和按身份限流暂不接入。

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
