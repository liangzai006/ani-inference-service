# ADR-0001：Kratos 控制入口与持久 Controller 工作

状态：accepted；日期：2026-09-12。用户已确定 Kratos gRPC + client-go + controller-runtime，并选择 InferenceService CRD 作为 PostgreSQL desired state 的 Kubernetes 投影；CRD 不取代 PostgreSQL 的业务权威。

## 比较

| 方案 | 优点 | 成本/不适用点 |
|---|---|---|
| PG 权威 + controller-runtime 监听原生对象 | 保留事务/幂等/operation；无需新集群级 CRD；符合本阶段只管理自有 runtime 的范围 | 必须有持久工作、启动重建和定期观察，不能只靠内存通知 |
| PG 权威 + 新 Inference CRD 投影 | 可用 `kubectl` 查看推理期望投影；以 CR 关联自有资源更直接 | PG→CR 重试/双版本时效、CR 删除/重建、schema/RBAC 升级均需新合同；仍不能省掉受理后的持久工作 |
| CRD 作为唯一业务权威 | Kubernetes 原生声明方式 | 需要重设租户业务事务、operation/幂等/审计的一致性，不符合当前保留完整 PG 本地事务的方向 |
| 直接复制旧 ANI Observer/Worker | 可以复用旧文件 | 用户已经要求替换，且保留 Core PlatformWorkload/GET 推进状态会继续耦合 |

采用第二项的受控变体：PostgreSQL 仍是业务权威，InferenceService CRD 是可重建的期望投影。controller-runtime 同时监听 CRD 与本服务拥有的原生 runtime 对象；Channel/框架队列不是可靠数据库事件投递，必须由 PostgreSQL 持久工作弥补丢失和重启窗口。参考 [controller-runtime source 官方说明](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/source)。

## CRD 投影时的具体工作方式

1. gRPC `Create/Update/Start/Stop/Delete` 在 PostgreSQL 事务中写入服务、spec、operation 和 `resource_work`；worker 以 typed CRD projection 反映当前 generation。CR 不是第二个业务写入入口，CR 变化只唤醒 worker，随后按 PostgreSQL 期望重写 projection。
2. Controller Manager 用 typed `InferenceService`、Deployment、Service、Pod、LWS Watch 唤醒服务。事件处理器根据 PostgreSQL binding 和 tenant/service labels 映射稳定 service key；label 只用于筛选，不能单独证明跨租户所有权。CR status 只能投影已持久化观察结果。
3. Reconcile 每次在 PostgreSQL lease 下重新读取当前 generation/desired state，再通过 typed client 创建/更新/删除 CR 与自有 runtime。绑定表中的 kind/name/UID/resourceVersion 是归属和并发核对依据；对象 UID 不匹配、缓存过期或查询失败时进入 unknown/stale，不自动收养或删除同名对象。
4. Controller 启动时先等待 K8s cache sync，再扫描所有未删除服务的 `resource_work`、未完成 operation 和到期观察任务，重新入队。运行中由数据库 due-scan 定期通过 Channel 投递，Controller 的 `RequeueAfter` 只做加速；因此用户不访问 GET、K8s 没发生事件或进程重启，都不会停止恢复。
5. Reconcile 将执行结果和观察结果写回 PostgreSQL。K8s 对象 Ready、模型加载探测、发布生效和数据面调用健康分别记录；operation 成功后仍然有独立周期观察。任何 watch/cache/API 故障都表达为 unknown/stale，不当成“对象不存在”来重建。

CRD 只提供 Kubernetes 侧查看和事件入口，不承担幂等、配额、审计、operation 或恢复账本；这些仍由 PostgreSQL 保存。CR 删除、重建和 schema 升级都必须按 projection drift 处理，不能触发跨领域接管。

## 装配和分层

Kratos 负责进程、gRPC/admin transport；Manager 作为其生命周期内的运行组件。`internal/service` 转换契约，`internal/biz` 维护领域决策，`internal/data` 承载 PG/K8s/外部能力。来源是固定 layout 的[运行合同](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/docs/runtime.md)和[生成器](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/scripts/new-service)，不是另建 ANI 公共 runtime。

Network 的持续观察方案可参考持久恢复、来源时效和业务归属，但它采用 client-go informer 的选择不替代本服务的 controller-runtime 决策。Network 的 IAM/容量/配额延期也不自动成为 Inference 豁免。参考其[观察 ADR](https://github.com/zhangzhe-ctrl/ani-network-service/blob/e481e968d3cc2f17bc4c6a736c438428519b09a0/docs/adr/0004-observe-cr-with-durable-reconciliation.md)。

具体行为、单活限制、失效恢复和测试只在[规格第 6、10 节](../specs/inference-service.md)维护；本 ADR 不再复制规则。
