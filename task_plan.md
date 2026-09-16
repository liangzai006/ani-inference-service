# Inference Operator 重构实施计划

## 目标
实现独立 Inference 服务的 gRPC → PostgreSQL → CRD → Operator → Kubernetes runtime → observation/quota/audit 闭环。

## 阶段
- [x] 阶段 1：冻结并记录设计、契约和边界
- [x] 阶段 2：生成固定 layout 骨架并验证运行合同（官方 Kratos/Buf 生成器已在临时目录验证；正式服务运行合同仍待复验）
- [x] 阶段 3：实现 PostgreSQL migration、sqlc 查询和本地状态机（包含真实 PG migration、Create 事务、幂等、租约/CAS 和周期观察验证）
- [~] 阶段 4：实现独立 gRPC 入口和幂等受理（Create/Get/GetOperation/List + Start/Stop/Restart/Delete/Update 及 PostgreSQL 受理、generation CAS 已完成；真实外部 provider 结果仍待验证）
- [~] 阶段 5：实现 CRD、controller-runtime Reconcile 和持久恢复（typed CRD、CR/runtime Watch 唤醒、Deployment/Service/LWS renderer 与 Service endpoint binding、PG lease、runtime binding CAS、typed SSA executor、Kratos LoopServer/ManagerServer、PG→runtime worker、lease-fenced 观察写回、主进程 active operation/terminal observation 选择已完成；真实 Kubernetes API、Service、缓存和调度仍待验证）
- [~] 阶段 6：实现 quota reservation、审计和 runtime 观察（本地 pending reservation、命令审计、不可变 observation history、runtime/LWS observation CAS、运行前配额/撤发布门禁、显式 operation runner/PG OperationStore、跨代 reservation/publication fencing、上一代 quota 释放和 delete tombstone 已完成；真实 Reserve/Confirm/Release、publication、模型/调用健康 provider 待接入）
- [~] 阶段 7：测试、真实依赖验证和状态记录（核心包、PG migration、构建检查和隔离 Kubernetes 1.36.2 envtest watch 已完成；LWS controller/GPU、引擎、数据面、IAM/Model/Quota 及进程级 crash 恢复未验证）

## 约束
- 只修改 /root/kubercon/ani-inference-service；旧 ANI 只读。
- 不使用 ani-core-platform，不复制旧 REST/OpenAPI/Proto/Core PlatformWorkload/Observer/Reconciler。
- 基于 layout commit fd18422211c741dd5242d2992c3d307d522aa28c。
- PostgreSQL + sqlc、无 RLS、显式 tenant_id。
- 不操作集群、远程仓库，不 commit/push。

持续循环目标和 30 秒中断重试规则见 [`docs/execution/GOAL.md`](docs/execution/GOAL.md)。
