# Inference 独立项目对话交接记录

更新时间：2026-09-11。该文件用于在新项目对话中恢复本次讨论上下文，不是 API 契约，也不代表实现或部署已经完成。

## 当前项目边界

- 当前开发仓库：`/root/kubercon/ani-inference-service`
- 旧 ANI 参考仓库：`/root/kubercon/ANI`，只读，不修改、不删除、不提交、不部署。
- 新仓库当前分支：`main`；尚未配置远程地址，未提交代码。
- 当前内容是设计文档和独立数据库 migration；运行服务代码尚未生成。
- 不修改前端，不修改旧 ANI v1 契约，不接管旧集群存量资源，除非后续明确授权。

## 已确认方向

1. 新 Inference 服务独立仓库、独立构建、独立版本、独立进程；基于固定 `ani-kratos-layout` 生成，生成后不依赖模板运行时。
2. 不使用 `ani-core-platform` 技能，不套用旧 ANI 的目录、Core PlatformWorkload、旧 REST/OpenAPI/Proto 或旧 Observer/Reconciler。
3. 使用 layout 自带的 Kratos gRPC transport；admin HTTP 只用于健康、就绪、指标和运维，不作为业务 API。
4. 新服务使用 `client-go + controller-runtime`；Network 的实际实现采用 `client-go dynamic + SharedIndexInformer`，不能误称它已经使用 controller-runtime。
5. 不默认新增 Inference 专属 CRD。PostgreSQL 保存业务期望、generation、operation、publication、观察和 `resource_work`；Kubernetes Deployment/Service/Pod/Job 等是执行事实。
6. 事件只负责唤醒，数据库中的持久工作、启动扫描和周期观察负责丢事件/进程重启恢复。operation 成功后仍须持续发现 runtime、模型和调用健康退化。
7. `accelerator` 可选：传入才使用卡；不传则正常启动，不强制增加 `--device=cpu`，也不静默把 GPU 请求降级为无卡。普通 CPU/内存 resource 字段是否保留待契约冻结，不能擅自解释为全部删除。
8. 新数据库使用 PostgreSQL + sqlc，不使用 RLS；所有租户业务表显式带 `tenant_id`，关联、查询、更新、唯一性和复合 FK 均限定租户。不复制 Model/IAM/Quota/Core 表。
9. 停止、重启、删除必须先撤销并确认 publication，再执行 runtime 变更；发布状态、runtime ready、operation 成功、调用健康分开表达。
10. 保留旧 Inference 的 generation、desired/applied spec、operation、publication、模型物化、GPU/LWS、自定义 argv、限流等能力，但重新实现，不复制旧代码。

## 固定来源

- layout commit：`fd18422211c741dd5242d2992c3d307d522aa28c`
- layout tree：`f18e1e7a04f5308563d88a0f54246a2d29622d9a`
- Network commit：`e481e968d3cc2f17bc4c6a736c438428519b09a0`
- Network tree：`57dcb56b6db4846a424c90ebe5ad9e1bea46ba19`
- 本地只读参考 clone：`/tmp/inference-layout-review.a0aFPb/layout`、`/tmp/inference-layout-review.a0aFPb/network`

layout 生成器会把 `cmd/server` 改名为 `cmd/<service-name>`，因此目标服务应使用 `cmd/ani-inference-service`。模板的 `internal/biz`、`internal/data`、`internal/service`、`internal/server` 结构应保留，不另造平行的 domain/application 层；按当前契约约定，所有业务和配置 Proto 统一位于 `api/inference/v1`，不在 `internal` 下放置 Proto 源文件。

## 当前新仓库文件

- `CONTEXT.md`：领域词汇。
- `docs/START-HERE.md`：文档入口。
- `docs/specs/inference-service.md`：行为、数据、接口、生命周期和验收草案。
- `docs/adr/0001-controller-and-authority.md`：无 CRD、PG 权威和 Controller 选型。
- `docs/plans/inference-service.md`：实施阶段和交接边界。
- `docs/execution/status.md`：当前状态。
- `migrations/000001_inference_control_plane.sql`：独立 schema 草案，尚未真实 PostgreSQL replay。
- `migrations/README.md`：migration 权威和凭据边界。

## Network 实际联动方式

Network 并没有使用 controller-runtime。它在 `cmd/ani-network-service/app.go` 中装配 PostgreSQL、dynamic Kubernetes client、`KCObservation`、业务 worker 和 Kratos 生命周期。

`internal/data/kc_observation.go` 为 VPC、Subnet、Pod、VNic、VNicIP、EIP 建立 SharedIndexInformer。Watch 事件只提取关系键并合并到内存 pending 集合，随后调用 `NotifyResource`/`NotifyAttachment` 增加 PostgreSQL 的 `requested_generation` 和下一次执行时间。

`internal/biz/worker.go` 和 `internal/data/worker.go` 再从 PostgreSQL Claim lease，执行 `Observe → Ensure/Delete → Finish`。定时完整 List 审计、启动恢复、租约和 generation 保证事件丢失或进程重启后仍可恢复。该模式可作为新 Inference 的持久联动参考，但资源状态机和 Provider 代码不能直接复制。

## 新项目对话启动提示词

在新项目打开 `/root/kubercon/ani-inference-service` 后，可粘贴：

```text
请先读取 README.md、CONTEXT.md、docs/START-HERE.md、docs/execution/status.md、docs/execution/records/CONVERSATION-HANDOFF-2026-09-11.md 和 migrations/000001_inference_control_plane.sql。

当前仓库是独立 Inference 服务。所有写入只能发生在当前仓库；旧 ANI 仓库 /root/kubercon/ANI 仅只读参考。不要使用 ani-core-platform，不复制旧 ANI REST/OpenAPI/Proto、Core PlatformWorkload 或旧 Observer/Reconciler，不修改前端和集群。

遵循 handoff 中已确认的 Kratos gRPC、PostgreSQL+sqlc 无 RLS、显式 tenant_id、client-go+controller-runtime、无默认 CRD、可选 accelerator 和 publication 先撤销规则。先说明本次会修改的文件、验证命令和未验证依赖，再执行；不要自动 commit、push 或部署。
```

## 当前未验证事项

- 新服务尚未按 pinned layout 生成。
- 新 Proto、sqlc 配置和 controller-runtime/client-go 版本尚未冻结。
- migration 尚未在真实 PostgreSQL replay，未做跨租户负向测试。
- 未连接真实 IAM、Model、Storage、Cluster/Inventory、数据面或限流系统。
- 未构建、部署或验证真实 Inference runtime；未进行旧资源切换。
- 独立阅读评审因模型容量限制未完成，不能标记为通过。
