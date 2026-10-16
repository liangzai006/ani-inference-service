# 新环境 Inference Helm 部署

正式安装只使用 Helm、`kubectl apply` 和本地子 Chart。Helm 默认使用当前
`~/.kube/config` 的 context，不在命令中传 `--kube-context`。安装前先显式核对当前
context，避免误操作旧环境。`scripts/install.sh` 和
`scripts/install-dependencies.sh` 只用于测试、离线演练和 CI，不是正式安装入口。

## 安装范围

正式 Helm/`kubectl` 流程安装以下组件：

- Gateway API 标准 CRD 和 Gateway API Inference Extension CRD；
- Higress 2.2.4、GatewayClass/Gateway、模型路由、统计、限流和 x-api-key 插件；
- LeaderWorkerSet 0.6.2；
- KServe 0.16 Core、LLMInferenceService CRD 和控制器；
- `ani-inference` Deployment、ServiceAccount、最小 Higress 限流 RBAC，以及向已有 ClusterIssuer 申请的零信任 Certificate。Chart 不创建签发者；RBAC 默认只允许控制面账号对指定限流 WasmPlugin 执行 `get`/`patch`。

Kubeflow 发行版保持 v1.10；KServe 单独使用 0.16 是为了让当前代码的
`LLMInferenceService` 和原生多机 LWS 链路可用。KServe 0.15 没有这组 LLMI
控制器 Chart。cert-manager 必须作为外部基础设施预先安装并保持就绪；本流程和
ANI Chart 不安装、升级或接管它。PostgreSQL、S3、Model Service 和 ClusterIssuer
仍属于外部公共依赖。在当前没有 Ceph 数据盘的新集群中，它们由独立的 `ani-infra`
基础设施层提供默认 StorageClass，不放进业务 Chart。KServe/LWS 的
admission webhook 使用 cert-manager。

同一个集群只能有一个 KServe 控制器管理这些集群级 CRD 和 webhook。Kubeflow
v1.10 如果已经启动了旧 KServe 0.15 控制器，正式安装前必须人工确认其迁移或停用；
不要直接用新 Chart 覆盖旧控制器。测试脚本会在写操作前检查冲突，但正式 Helm 命令
不会自动删除或缩容现有控制器。

Gateway API 没有上游 Helm Chart，正式命令直接应用已下载到
`charts/vendor/` 的官方 CRD 清单。其余依赖的官方 Helm 包也下载到同一
目录，并通过 `SHA256SUMS` 校验；可以先在有网络的开发机执行下载，再把
`charts/vendor/` 带到部署机。

## 新环境前提与安装顺序

Model 和 Inference 必须在同一个新集群、同一个 `ani-system` 命名空间中，按下面顺序安装：

1. 确认外部 cert-manager 和 `ClusterIssuer/ani-internal` 已经 Ready，再用下面的
   Helm/`kubectl apply` 命令安装 Gateway API、LWS、Higress、KServe 和 LLMI，
   并创建、标记 `ani-system`。这些命令不会安装或接管 cert-manager，也不会安装 ANI Deployment。
2. 确认公共 `ClusterIssuer/ani-internal` 已经存在且 Ready，然后安装
   `ani-model-service` 仓库的 Model Chart。Model Chart 会申请
   `ani-model-server-tls`，并在启动时检查 PostgreSQL 和 S3。
3. 最后设置网关地址和 API Key，用 Helm 安装 Inference Chart，申请客户端证书并启动服务。

Model Chart 的命令和 S3 参数见 Model 仓库的
`docs/deploy/helm/ani-model.md`。正式安装不要给 Helm 传 `--kube-context`，只使用
当前 kubeconfig context。

Gov 调用普通 Inference gRPC 时必须使用由 `ani-internal` 签发、DNS SAN 为
`ani-governance` 的客户端证书；客户端证书只能包含这一项 DNS SAN，不接受额外的
DNS、URI、IP 或 email SAN。每次请求携带唯一的
`x-ani-tenant-id` 和 `x-ani-actor`（值以 `governance:` 开头）。Inference 会把这些
元数据绑定到请求上下文，不能用请求体字段替代。Model 调用引用检查时同样必须使用
已签发的客户端证书和租户元数据。

安装前还要准备：

- cert-manager：由平台基础设施提前安装并保持就绪。下面的命令只读取 CRD 和 Deployment 状态，不安装、升级或接管；如果只有部分 CRD/Deployment，应先停止并修复基础设施；
- `ani-system/inference-database` Secret，键名 `dsn`；
- 公共 ClusterIssuer `ani-internal`。Chart 用它向 cert-manager 申请普通 gRPC 服务端证书 `ani-inference-server-tls`（DNS SAN `ani-inference.ani.internal`）和调用 Model 的客户端证书 `inference-model-mtls`（DNS SAN `ani-inference.ani.internal`）；Model 对端身份是 `ani-model.ani.internal`。不要手工创建这两张证书；
- `ani-system/ani-model-grpc` Service；
- Model Chart 使用的 S3/Ceph 访问配置；Inference 模型 PVC 不指定 `model.storageClass`，使用集群默认 StorageClass；
- 如果镜像仓库需要认证，先在 `ani-system` 创建镜像拉取 Secret；安装时通过 `imagePullSecrets` 传入 Secret 名称。Chart 会把它挂到独立的 runtime ServiceAccount，物化 Job 和 KServe 推理 Pod 共用该账号；该账号没有控制面 Role；
- 依赖镜像已同步到新环境可访问的镜像仓库。

当前 values 已把 KServe、LLMI 和 LWS 控制面镜像指向
`docker.changqingyun.cn/mirror`，Higress 也使用该镜像前缀。执行安装前确认这些
平台依赖镜像已经通过 ORAS 同步到目标仓库；模型引擎镜像仍由创建推理的请求传入，安装脚本
不会替换它。

如果目标仓库路径不同，只修改 `charts/platform-values/*.yaml` 和
`docs/deploy/higress/new-cluster-values.yaml` 中的镜像地址，再执行上面的 Helm 命令。

## 正式安装平台依赖

下面的命令就是正式安装入口。它们不传 `--kube-context`，Helm 和 kubectl 都使用当前
`~/.kube/config` context；`KUBE_CONTEXT` 只用于安装前的显式核对。

```bash
cd /root/kubercon/ani-inference-service
export KUBE_CONTEXT="${KUBE_CONTEXT:?填写新集群 context}"
test "$(kubectl config current-context)" = "$KUBE_CONTEXT" || {
  echo "当前 kubeconfig context 不是目标新集群" >&2
  exit 1
}
export ANI_HIGRESS_API_KEY="${ANI_HIGRESS_API_KEY:?填写新环境 API Key}"

# cert-manager 是外部基础设施；这里只做只读前置检查，不执行安装或升级。
kubectl get crd certificates.cert-manager.io issuers.cert-manager.io clusterissuers.cert-manager.io
kubectl -n cert-manager get deployment cert-manager cert-manager-webhook cert-manager-cainjector
kubectl -n cert-manager rollout status deployment/cert-manager --timeout=5m
kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=5m
kubectl -n cert-manager rollout status deployment/cert-manager-cainjector --timeout=5m

kubectl apply --server-side --field-manager=ani-inference-platform \
  -f charts/vendor/gateway-api-v1.6.0-standard-install.yaml \
  -f charts/vendor/gateway-api-inference-extension-v0.3.0-manifests.yaml

helm upgrade --install lws charts/vendor/lws-v0.6.2.tgz \
  --namespace lws-system --create-namespace \
  --values charts/platform-values/lws.yaml --wait --timeout 15m

helm upgrade --install higress charts/vendor/higress-2.2.4.tgz \
  --namespace higress-system --create-namespace \
  --values docs/deploy/higress/new-cluster-values.yaml \
  --wait --timeout 15m
kubectl apply -f docs/deploy/higress/gateway.yaml
kubectl apply --server-side --field-manager=ani-inference-platform \
  -f docs/deploy/higress/plugins.yaml
sed "s|\${ANI_HIGRESS_API_KEY}|${ANI_HIGRESS_API_KEY}|g" \
  docs/deploy/higress/key-auth.yaml.tmpl | kubectl apply -f -

helm upgrade --install kserve-crd charts/vendor/kserve-crd-v0.16.0.tgz \
  --namespace kserve --create-namespace --wait --timeout 15m
helm upgrade --install llmisvc-crd charts/vendor/llmisvc-crd-v0.16.0.tgz \
  --namespace kserve --wait --timeout 15m
helm upgrade --install kserve charts/vendor/kserve-v0.16.0.tgz \
  --namespace kserve --values charts/platform-values/kserve.yaml \
  --post-renderer scripts/drop-kserve-configmap-post-renderer.py \
  --post-renderer-args="--mode=core" --wait --timeout 15m
helm upgrade --install llmisvc charts/vendor/llmisvc-v0.16.0.tgz \
  --namespace kserve --values charts/platform-values/llmisvc.yaml \
  --post-renderer scripts/drop-kserve-configmap-post-renderer.py \
  --wait --timeout 15m
helm upgrade --install ani-inference-config charts/ani-inference-config \
  --namespace kserve --wait --timeout 15m

kubectl create namespace ani-system --dry-run=client -o yaml | kubectl apply -f -
kubectl label namespace ani-system \
  ani.liangzai006.io/environment=new-cluster --overwrite
```

## 正式安装 Inference

```bash
export HIGRESS_PUBLIC_BASE_URL="${HIGRESS_PUBLIC_BASE_URL:?填写新环境网关地址}"
helm upgrade --install ani-inference charts/ani-inference \
  --namespace ani-system --create-namespace \
  --set-string higress.publicBaseURL="$HIGRESS_PUBLIC_BASE_URL" \
  --wait --timeout 15m
kubectl -n ani-system rollout status deployment/ani-inference-service --timeout=15m
```

如果只需要下载依赖或在 CI 中演练安装，才使用 `scripts/fetch-dependencies.sh`、
`scripts/install-dependencies.sh` 和 `scripts/install.sh`。这些脚本会使用显式
`KUBE_CONTEXT` 隔离测试集群，不能作为正式部署入口。

## 引擎参数边界

上游 LLMI Chart 自带的示例 `LLMInferenceServiceConfig` 会写入固定的 vLLM
命令。上面 Helm 命令通过 Helm 的 `--post-renderer` 参数删除这些示例命令，只保留空的
配置占位，并删除 LLMI Chart 与 Core Chart 重复的 `inferenceservice-config`。
这是 Helm 渲染步骤，不是服务安装脚本。

创建推理时，前端向 Gov 提交完整的 `command` 字符串，Gov 转发到 Inference
的 `engine.command`。选择模型版本可以回填命令，提交前仍可临时修改，例如：

```json
{"command": "vllm serve /models --served-model-name qwen2.5-1.5b-instruct"}
```

Kubernetes/KServe 适配器仅将该字符串放入固定 shell 包装：

```yaml
command: ["/bin/sh", "-c"]
args:
  - vllm serve /models --served-model-name qwen2.5-1.5b-instruct
```

镜像必须包含 `/bin/sh`。业务接口不接收命令数组或独立的 `engine.args`；
服务不会拆解命令、追加 `exec`、拼接或补充任何引擎启动参数。

## 数据库迁移

`ani-inference` Chart 在安装前用 Hook 按 `charts/ani-inference/migrations/` 顺序执行迁移。这不是单独的 Helm release，也不创建数据库。`docs/deploy/foundation/` 里的 Job 只在不走 Chart Hook 时手工使用。

## 安装后验证

```bash
kubectl -n ani-system \
  get pods,svc,sa,role,rolebinding
kubectl -n kserve \
  get inferenceservices,llminferenceservices,llminferenceserviceconfigs
kubectl -n higress-system \
  get gateway,wasmplugin
```

`scripts/preflight.sh` 只在测试/CI 中使用；它是只读检查，不是安装命令。真实模型导入、
引擎启动和网关请求仍需单独做业务验收。

## 卸载

卸载 ANI 服务不会自动删除平台依赖。确认没有其他服务使用后，才按相反顺序手动
卸载对应 Helm release；不要直接删除 `kserve` 或 Gateway API
CRD，也不要卸载公共的 cert-manager、PostgreSQL 或 Ceph/S3。旧环境必须使用不同的 `KUBE_CONTEXT`，不能复用新环境命令。
