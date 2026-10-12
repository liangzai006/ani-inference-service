# 新环境安装说明

正式安装入口是 `ani-inference.md` 中的 Helm、`kubectl apply` 和本地子 Chart
命令。`scripts/install.sh`、`scripts/install-dependencies.sh` 和
`scripts/preflight.sh` 只用于测试、离线演练和 CI，不作为正式安装入口。

## 安装前

正式安装使用当前 kubeconfig context。执行前显式核对 context：

```bash
cd /root/kubercon/ani-inference-service
export KUBE_CONTEXT='新集群的 context'
test "$(kubectl config current-context)" = "$KUBE_CONTEXT"
```

PostgreSQL、Ceph/S3、Model Service 和 `ClusterIssuer/ani-internal` 是外部业务前提。
cert-manager、Gateway API、LWS、Higress、KServe 和 LLMI 按
`ani-inference.md` 中的 Helm 命令安装或复用；发现部分安装时停止，不覆盖未知资源。

## 安装

按 [`ani-inference.md`](ani-inference.md) 中的“正式安装平台依赖”和“正式安装
Inference”两节逐条执行。每条安装动作都是 Helm 或 `kubectl apply`，Helm 不传
`--kube-context`。

所有 ANI 业务资源固定在 `ani-system`。依赖控制面使用 `kserve`、`lws-system` 和
`higress-system`。脚本不会访问旧环境。

## 单独检查

```bash
kubectl -n ani-system get pods,svc,certificate
kubectl -n kserve get pods
kubectl -n higress-system get gateway,wasmplugin
```

`preflight.sh` 只读检查 CRD、控制器、网关、限流插件、数据库 Secret、Model
Service、ClusterIssuer 和 Inference 客户端证书，不会创建、修改或删除资源。

## 数据库迁移

迁移由 `ani-inference` Chart 的 pre-install/pre-upgrade Hook 执行。迁移账本位于
目标数据库的 `public.inference_schema_migrations`，每个迁移文件与账本写入在同一个事务中，
重复执行会跳过已完成文件。需要跳过迁移时，显式传入：

如果检测到旧版本已经完整执行但没有账本，Hook 会根据最新表和字段建立一次
基线；发现部分旧 schema 会停止并要求先人工完成迁移，不会覆盖或重复执行旧表。

```bash
helm upgrade --install ani-inference charts/ani-inference \
  --namespace ani-system --set migration.enabled=false
```

正式安装不要调用 `scripts/install.sh` 单独安装平台组件；必须保持文档中的版本和
资源顺序一致。
