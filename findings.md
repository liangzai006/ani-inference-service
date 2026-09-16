# Findings

- 当前仓库已包含 layout 骨架、gRPC/CRD/PG/Controller 代码；完整 runtime/publication/provider 与正式切换仍未完成。
- 用户确认采用 InferenceService CRD + Operator；Inference 自己操作 Kubernetes 并落 PostgreSQL。
- gRPC ResourceSpec 遵循 Kubernetes ResourceRequirements：requests/limits 为 map<string,string>，GPU 直接使用 nvidia.com/gpu 等扩展资源名；Accelerator 字段删除。
- 配额必须支持，但全局配额权威仍按准则归 Core 治理；Inference 保存 reservation 引用/状态并调用 Reserve/Confirm/Release 协议。
- 审计必须是追加写入；当前 migration 使用独立 `inference_audit_events`，可靠 outbox 是否需要在跨服务事件接线时另行增加。
- Network 方案的可复用部分是 durable worker、lease、启动扫描、周期 List、Observe/Ensure/Delete；Network 本身使用 client-go dynamic + SharedIndexInformer，不是 controller-runtime。
- 本轮已补上跨代写回 fencing、旧 generation publication/quota 查询、running update/restart 的上一代配额释放步骤、delete tombstone、观察 freshness 和 operation runtime binding 事实传递；这些仍需要真实生命周期故障注入验证。
- 当前不能宣称产品链路完成：主进程未接真实 IAM/admission、Core quota、publication/data-plane、Model artifact/materialization 或 engine model/invocation health provider；Service 显式 endpoint 已完成 gRPC/CRD/PG 持久化和 typed apply/binding，但真实 API、publication 地址确认及 TLS/鉴权仍未验证，Job runtime 仍未实现。缺少这些依赖时 runner 持久重试并 fail closed。
