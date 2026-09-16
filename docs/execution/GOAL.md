# Inference 重构持续目标

这是当前实现循环的唯一目标提示词：

> 在 `/root/kubercon/ani-inference-service` 独立仓库内完成 Inference operator/CRD 重构并通过验收。保留固定 layout SHA `fd18422211c741dd5242d2992c3d307d522aa28c`、Kratos gRPC、PostgreSQL+sqlc（无 RLS，所有租户查询显式 `tenant_id`）、client-go+controller-runtime 与官方 LWS typed API。禁止修改旧 `/root/kubercon/ANI`、旧 Core PlatformWorkload/Observer/Reconciler、前端、远程仓库、commit/push；当前集群可用于隔离验证，但不得未经切换清单接管存量资源。
>
> 实现完整闭环：gRPC Create/List/Get/Update/Start/Stop/Restart/Delete/GetOperation/ListOperations 的幂等与 generation CAS；PG desired/spec/operation/runtime binding/observation/publication/quota reservation/audit/resource_work 持久状态；持久 worker、controller-runtime CR/runtime watch、数据库 due scan、启动和完整 List 恢复；typed Deployment/Service/LWS/Pod 执行，UID/resourceVersion/lease/generation fencing；模型物化、Kubernetes resources requests/limits、GPU 扩展资源、自定义 argv、runtime ready/model load、publication withdraw/confirm/publish、invocation health；stop/restart/delete 必须撤发布并确认后才能停删 runtime，确认消失后才能释放 quota；配额权威 adapter 的 Reserve/Confirm/Release 重试补偿与审计事件。
>
> 每轮循环先读取 `docs/execution/status.md` 和本目标，列出剩余 `not_verified`，只选一个可验证切片实现；然后在开发机网络环境运行 gofmt、sqlc/buf（需要时）、定向测试、`go test -p 1`、`go vet`、`go build` 和真实本机 PostgreSQL 验证，并更新状态和执行记录。中断后 30 秒重试同一切片；失败先定位再重试，不把设计草案或单测替代真实依赖证据。非高危审批自动继续；`rm`、清库、集群部署/切流、远程写入等高危或不可逆操作停止并汇报。持续循环直到所有必需能力通过，或明确记录外部依赖阻塞。

正式运行不得使用固定模型、固定配额或 development profile。Inference 只保存模型和配额服务返回的引用、版本、预留及结算结果；Model、Quota、Publication 和 IAM 均通过明确版本化的外部服务契约接入。未配置或未验证这些权威依赖时，进程必须保持业务未就绪，测试替身只能出现在隔离测试代码中。

## 当前执行顺序

1. 运行中 update/restart 的上一代 quota 释放、旧 publication 撤回、新代发布和删除 tombstone 已有真实 PG 生命周期证据；继续补 provider 成功但结果未落库、真实进程中断和 lease 丢失后的恢复验证。
2. PostgreSQL due-scan、启动恢复和 worker/controller manager 生命周期接线。
3. [ADR 0002](../adr/0002-service-endpoint-contract.md) 的内部 endpoint 合同已进入 gRPC/CRD/PG/sqlc，并完成 typed Service apply、CR endpoint 投影、endpoint binding 和删除观察；真实 Service API、publication/data-plane 地址、TLS/鉴权协议仍需外部依赖验证。
4. quota Reserve/Confirm/Release、审计状态转移和补偿；本地 worker 已按持久 `pending/reserved/confirmed` 状态恢复，支持 authority 查询和明确 `not_found` 分类，仍需接入版本化 Core authority 协议和真实依赖验证。
5. 模型物化、runtime ready/model load、调用健康与完整 operation step 状态机。
6. 完整 gRPC 命令、列表投影（含 ListOperations）已完成；剩余为鉴权接线和真实依赖测试。
7. 低基数领域指标已接入（积压、重试、观察 stale、模型物化时延/依赖错误）；继续复盘
   所有 `not_verified`，只在有证据时更新为 `pass`。状态投影的单语句 PG 快照、
   stale-after、RV 冲突已通过真实 PG/隔离 API 验证；首次状态投影的
   PG control binding UID 核验已通过真实 PG 和隔离 API 回归；CR spec apply、
   DeleteCR 的 UID/current-RV fencing 也通过隔离 API 回归；Deployment、Service、
   LWS 的 GET→apply 并发 Conflict 已通过加载官方 CRD 的隔离 envtest；官方 LWS
   controller 创建 StatefulSet 的隔离 envtest 也已通过。Job watch 已明确为
   wake-only，GPU 调度、Pod readiness 和真实多节点行为仍待隔离集群验证。持久审计上下文
   的内部传播已完成，IAM Actor 注入和外部 provider 仍为 not_verified。外部 provider 和集群验证
   必须使用隔离真实依赖。
