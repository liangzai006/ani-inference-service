# 2026-09-12 status control identity

## 缺陷与修正

此前只保护一次调用期间的 UID/标签变化。新调用仍可按相同 tenant/service
标签读取 PG，把旧服务状态写给同名重建 CR 或不同 namespace/name 的对象。

状态查询现在在同一条 sqlc SELECT 中读取最新的 `InferenceService/control`
binding，关联包含 tenant_id、service_id、kind、role，并限制
`binding.generation <= desired_generation`。取最新一条后再比较身份，不能先
按调用对象的 UID 过滤后回退到历史 binding。stop/update 会先推进 desired
generation，因此新代 CR apply 前允许仍绑定的上一代 CR 投影真实状态。

`StatusProjector` 复用已有 `RuntimeBinding`，在每次写入前核对 kind、role、
namespace、name 和非空 UID；缺失或不符时拒绝写入。已 tombstone 的服务不返回
状态。此查询要求使用 PG 主库；control binding 与状态来自同一语句快照。

持久 binding resourceVersion 不作相等要求：它记录的是 spec apply 时的值，
正常 status 写入会推进 API resourceVersion。写入并发仍使用当前 APIReader
读取的 resourceVersion 和 SDK optimistic-lock patch，冲突时完整重读。
没有新增表、migration、依赖或填充另一份 `inference_runtime.cr_uid` 权威。

## 验证

- `TestStatusProjectorRequiresPersistedControlIdentity` 使用真实本机 PG 和
  Kubernetes fake client：修复前缺失 binding、UID/namespace/name 不符、已被新
  binding 替代的历史 UID 和 tombstone 共六项失败，修复后全部通过。
- 同一测试验证相同 UID 在 resourceVersion 推进后仍可投影、上一代 binding
  在 desired 推进后仍有效、未来代 binding 不参与选择以及最新 binding 可投影。
- 现有 status aggregate、旧 publication phase/effective、时效和 tenant 隔离
  的真实 PG 测试通过。sqlc 已从唯一查询来源重新生成。
- 隔离 Kubernetes 1.36.2 API server/etcd 验证全新调用拒绝已重建 UID，并保留
  调用中重建、标签改变、并发 status 冲突重读和相同状态不重写的测试。
  envtest source 为 fixture；PG binding 测试的 Kubernetes client 为 fake，
  这两层证据分别证明 SQL/身份规则与真实 API 行为。
- 最终同时设置 `INFERENCE_PG_DSN`、`KUBEBUILDER_ASSETS`、`LWS_CRD_PATH`
  执行 `go test -p 1 ./... -count=1` 全部通过：Kubernetes 包 15.825s，PG 包
  4.876s。随后 `go vet -p 1 ./...`、`go build -p 1 ./...` 通过，gofmt 无输出。
  使用已有本机资产、`UseExistingCluster=false`，没有连接现有集群。

## 后续边界

- 仍不是 PG 与 Kubernetes 的跨系统事务，也不是控制业务状态的 status writer。
- 代码审查另外发现 `RuntimeExecutor.applyCR` 在 SSA 前只检查标签和 binding
  是否存在，未核对持久 UID。可能先修改同名替代 CR，再在落库时遭 CAS 拒绝。
  该独立 spec 写入缺陷下一切片修复；status 投影通过不代表它已通过。
- `DeleteCR` 还把 spec apply 时保存的 RV 与最新对象 RV 比较；status 写入后
  二者正常不等，可能持续拒绝删除。应核对持久 UID/对象定位，使用新读取的 API
  RV 作删除 precondition，并验证 GET→Delete/SSA 之间重建的竞态。
- 正式进程中断、实际 quota/Model/Storage/publication/IAM、LWS/GPU、引擎和
  数据面链路仍为 `not_verified`；现有集群、旧 ANI、远程仓库均未操作。

## CR fencing 证据（同日后续切片）

`RuntimeExecutor.applyCR` 现在在 SSA 前核对持久 control binding 的 kind、role、
namespace、name 和 UID；同名替代对象会返回 stale generation，且不会发生 spec
写入。对仍属于同一 UID 的对象，SSA 使用 APIReader 刚读取的 resourceVersion；
control binding 保存的旧 RV 不再被用于 status 后的 spec/delete 判断。

`DeleteCR` 同样核对持久 UID 和对象定位，但删除 precondition 使用本次 APIReader
读取的 UID/RV。隔离 Kubernetes 1.36.2 envtest 已验证：status 子资源推进 RV 后
仍能删除，以及旧 binding 面对同名替代 CR 时在 SSA 前拒绝。相应 fake 回归也通过。

runtime Deployment/Service/LWS 的 apply 对象也已携带 APIReader 刚读取的
resourceVersion；UID 先在读后比较，RV 由 Kubernetes API server 作为 SSA 乐观锁。
单元回归确认发送的 apply patch 带 fresh RV；真实 runtime 对象在 GET→SSA 之间
发生普通并发写入时的 Conflict 证据仍待隔离 API 回归。

本轮 `TestRuntimeExecutorApplyCarriesFreshResourceVersion` 先在缺少该字段时失败，
修复后确认 Deployment SSA 请求带有 APIReader 返回的 `resourceVersion=41`；
同样的赋值已用于 endpoint Service。随后重新执行全量
`go test -p 1 ./... -count=1`（PG 与 envtest）、`go vet -p 1 ./...`、
`go build -p 1 ./...` 和改动文件 gofmt，全部通过。该单测未模拟 API server
在 GET→SSA 窗口内的并发写入，因此该项在本轮随后 envtest 中补齐。

隔离 envtest 加载官方 LWS CRD，并由 `runtimeRaceApplyClient` 在 SSA 转发前推进
对象 RV。Deployment、Inference endpoint Service 和 LeaderWorkerSet 均返回
`apierrors.IsConflict`，并发写入的 annotation 保留，Inference spec 没有被旧
desired 覆盖。该测试证明三种 typed 对象的 API schema/SSA/RV fencing；没有启动
LWS controller，Pod 调度、GPU、worker readiness 仍不在本证据范围。

同一验证还暴露并修复 LWS renderer 缺少官方必需的
`spec.rolloutStrategy.type=RollingUpdate`：typed default 不会替代 API server
schema 的必填校验。修复位于 `internal/data/kubernetes/renderer.go`。
