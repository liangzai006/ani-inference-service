# 2026-09-12 status projection concurrency

本轮聚焦 PostgreSQL → CR status 的读取一致性与并发写入。旧 ANI、现有集群、
前端和远程仓库均未操作；没有 commit/push。

## 复现与修复

1. 原来的 `client.MergeFrom(current)` 不会自动将相同的 `resourceVersion`
   写入 patch。单独加入冲突重试仍可覆盖较新的 CR status。
2. 使用 SDK 自带的 `MergeFromWithOptimisticLock`，每次冲突都重新读取 API
   对象和 PostgreSQL 状态。若调用期间 UID 或 tenant/service 标签改变，终止
   当前投影，避免把旧结果写给重建或改变归属的对象。
3. PG adapter 从四次独立查询改为一个 sqlc SELECT，service、runtime、latest
   publication 和 current operation 使用同一语句快照；每个关联保留 tenant 条件。
4. 旧 generation 的 publication phase 继续表达真实事实。它仍 published 时
   不得伪装 withdrawn；`effective` 只有同 desired generation 且 desired/observed
   都 published 才为 true。

## 验证证据

`TestStatusProjectorEnvtestConcurrency` 使用本机已存在的 Kubernetes 1.36.2
API server/etcd，明确设置 `UseExistingCluster=false`。只安装本仓库测试 CRD，
不读取 kubeconfig、不接入现有集群；测试中的状态 source 是 fixture。

- 修复前：并发状态被回退到 generation 3；同名新 UID 也收到旧 pending 状态，
  两个回归测试均 `fail`。
- 修复后定向验证 `pass`：冲突重新读取后保留 generation 6；同名重建被拒绝；
  status 子资源保留 staleAfter；spec、UID、metadata.generation 不变；相同状态
  不产生额外写入。
- 实际 PG 回归覆盖旧发布 phase 保留而 effective=false、新代发布 effective=true、
  observed_at/stale_after 和跨租户查询返回 `pgx.ErrNoRows`。
- 全量启用 envtest 暴露测试之间的 controller 同名注册冲突。只在不启动 manager
  的构造测试里设置 SDK `SkipNameValidation`，正式服务校验不变。
- 修正后全量 `go test -p 1 ./... -count=1` 同时开启真实 PG 和 envtest，退出码 0；
  Kubernetes 包 12.793s、PostgreSQL 包 4.542s。`go vet -p 1 ./...`、
  `go build -p 1 ./...` 通过，改动文件 gofmt 无输出，sqlc 已重新生成。

可复现命令（PostgreSQL DSN 由本机环境注入）：

```sh
KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.36.2-linux-amd64 \
LWS_CRD_PATH=/root/.config/.gvm/go/gopath/pkg/mod/sigs.k8s.io/lws@v0.10.0/config/crd/bases \
GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1
```

## 仍未完成

- 此证据覆盖调用期间的 UID/租户变化。首次投影仍未与 PG 已持久化的 control
  binding UID 核对，不能据此声称新一次调用会拒绝所有冒用相同标签的 CR。
  后续切片已补此项，最新证据见[绑定身份记录](2026-09-12-status-control-identity.md)。
- 单条查询只保证 PG 语句快照；它不是 PostgreSQL 与 Kubernetes 的跨系统事务。
- 真实 provider、部署集群、LWS/GPU、模型加载及数据面均没有被本测试验证。
- CR `observedGeneration` 当前使用 PG applied_generation；消费者命名/语义冻结
  仍需独立验收。Kubernetes metadata.generation 是 spec 变化计数，不是 resourceVersion。
