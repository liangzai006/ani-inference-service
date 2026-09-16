# 2026-09-10 设计来源与检查记录

## 范围

本记录只证明固定源码阅读和本地设计检查，不证明新 Inference 已构建、测试、部署或切换。来源仓库只读克隆到临时目录，没有当成目标服务或运行依赖。未读取集群凭据、未操作集群。

## 固定输入

| 输入 | commit | tree |
|---|---|---|
| ANI 本地 main | `d503f2f34aa370bb089fca0003b917942b621370` | `0da251744c85b2725c624249bd7809c42ef9c01b` |
| ani-kratos-layout | `fd18422211c741dd5242d2992c3d307d522aa28c` | `f18e1e7a04f5308563d88a0f54246a2d29622d9a` |
| ani-network-service | `e481e968d3cc2f17bc4c6a736c438428519b09a0` | `57dcb56b6db4846a424c90ebe5ad9e1bea46ba19` |

layout 的 `layout-0-candidate` tag 本次远端查询为 `cf99284aa300132770a405d1d7dabd4728d33c42`，不冒充本次检查的 main SHA。本次选用的 SHA 与 Network 生成 provenance 所列 layout SHA 相同。

## 主源

- [layout 生成器](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/scripts/new-service)：固定 CLI 检查、cmd 改名、独立 module、CI/provenance 和无 runtime 模板依赖。
- [layout 显式装配](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/cmd/server/app.go)与 [gRPC server](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/internal/server/grpc.go)：一套 Kratos transport；无需自建 gRPC server。
- [layout 运行合同](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/docs/runtime.md)、[go.mod](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/go.mod)、[上游来源](https://github.com/zhangzhe-ctrl/ani-kratos-layout/blob/fd18422211c741dd5242d2992c3d307d522aa28c/docs/scaffold/upstream-provenance.md)。
- [Network 导航](https://github.com/zhangzhe-ctrl/ani-network-service/blob/e481e968d3cc2f17bc4c6a736c438428519b09a0/docs/START-HERE.md)、[生成来源](https://github.com/zhangzhe-ctrl/ani-network-service/blob/e481e968d3cc2f17bc4c6a736c438428519b09a0/docs/scaffold/provenance.md)、[观察 ADR](https://github.com/zhangzhe-ctrl/ani-network-service/blob/e481e968d3cc2f17bc4c6a736c438428519b09a0/docs/adr/0004-observe-cr-with-durable-reconciliation.md)。
- [Network 当前记录](https://github.com/zhangzhe-ctrl/ani-network-service/blob/e481e968d3cc2f17bc4c6a736c438428519b09a0/docs/execution/status.md)已将 NET-05A 写为 `completed_with_deferred_capacity`，不再是只有待实施设计；其中 IAM 仍延期，容量/历史门禁未全绿。这是其记录陈述，未在本任务重跑。
- [controller-runtime 官方 source 文档](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/source)：Kind/Channel 的用途，用于验证“不要求新 CRD”；未据网页 latest 选定项目依赖。

模板当前源码使用 Go `1.26.7`、Kratos `v3.0.0`；生成器固定 Kratos CLI module `github.com/go-kratos/kratos/cmd/kratos/v3@v3.0.0-20260626125723-668db92c2c00`，Buf `1.60.0`。这些来自源码，不表示当前本机工具安装/生成门禁已验证。client-go/controller-runtime 组合尚未冻结。

## 本轮澄清

旧笔记中的“必须新增 CRD”“生成后保留 cmd/server”“leader election 即互斥”“cpu 去掉等于删除所有 CPU 字段”都不能视为已确认事实。本次草案已分别更正为可无 CRD、生成器实际目录、需独立 fencing 边界和资源字段待冻结。用户明确排除 ani-core-platform 技能，旧 ANI 约束不用于限制这个独立服务。

## 旧实现迁移依据

以下对应表中 ANI SHA，链接为本次审计工作区绝对路径。清单证明源码结构，不证明集群当前功能正常；将来移动本包时保留 SHA 并更新源码定位。

| 迁移项 | 源码依据 | 新服务注意点 |
|---|---|---|
| desired/applied、generation、operation、publication | [资源](/root/kubercon/ANI/repo/services/inference-service/internal/domain/resource.go:158)、[状态转移](/root/kubercon/ANI/repo/services/inference-service/internal/domain/transition.go:33) | 不只是搬 CRUD；抢占和补偿规则需显式映射 |
| 创建事务与幂等 | [事务](/root/kubercon/ANI/repo/services/inference-service/internal/repository/postgres.go:565) | resource + operation + replay 的一致性不能丢失 |
| Core bootstrap/配置 | [入口](/root/kubercon/ANI/repo/services/inference-service/main.go:12)、[配置](/root/kubercon/ANI/repo/services/inference-service/internal/config/config.go:9) | 用自身 Kratos 装配替换，不携带旧 bootstrap 与通用依赖 |
| Core 执行/凭据、旧 Proto | [Core adapter](/root/kubercon/ANI/repo/services/inference-service/internal/runtime/coresdk/adapter.go:57)、[gRPC](/root/kubercon/ANI/repo/services/inference-service/internal/grpcapi/server.go:10) | 新 runtime 不走 PlatformWorkload，不复制 monorepo 生成契约 |
| RLS 与双 pool | [租户变量](/root/kubercon/ANI/repo/services/inference-service/internal/repository/postgres.go:21)、[claim](/root/kubercon/ANI/repo/services/inference-service/internal/repository/postgres.go:1011)、[PG fixture](/root/kubercon/ANI/repo/services/inference-service/internal/repository/postgres_integration_test.go:81) | 旧集中手写 SQL 不是 sqlc；迁移、查询、测试一起去掉 RLS 假设，保留 tenant 谓词和事务并发保护 |
| 完成后健康观察缺口 | [worker 入口](/root/kubercon/ANI/repo/services/inference-service/internal/reconcile/worker.go:79) | 此入口只领取 operation；不能由操作期间 Health/Smoke 推断完成后仍巡检 |
| 撤发布顺序 | [撤发布检查](/root/kubercon/ANI/repo/services/inference-service/internal/reconcile/worker.go:94)、[目标版本判据](/root/kubercon/ANI/repo/services/inference-service/internal/reconcile/worker.go:555) | stop/restart/delete 先确认撤销；等待撤销不能触发 runtime cleanup |
| 删除确认与回滚 | [delete](/root/kubercon/ANI/repo/services/inference-service/internal/reconcile/worker.go:146)、[rollback](/root/kubercon/ANI/repo/services/inference-service/internal/reconcile/worker.go:410) | 发送删除不等于已经消失；补偿也要可恢复 |
| GPU/LWS/Ray | [拓扑](/root/kubercon/ANI/repo/services/inference-service/internal/runtime/topology.go:38)、[旧 K8s 渲染](/root/kubercon/ANI/repo/pkg/adapters/runtime/kubernetes_platform_workload_runtime.go:863) | 保留整卡/vGPU、拓扑/gang/scheduler 的能力清单，但不复用旧 provider |
| 启动命令 | [完整 argv](/root/kubercon/ANI/repo/services/inference-service/internal/engine/launch.go:34)、[旧默认参数](/root/kubercon/ANI/repo/services/inference-service/internal/engine/launch.go:82) | 旧 vLLM 注入 CPU env，旧 SGLang 加 --device cpu；都不能直接继承为新正常启动默认 |
| 模型物化 | [catalog](/root/kubercon/ANI/repo/services/inference-service/internal/catalog/modelsvc/adapter.go:75)、[引用验证](/root/kubercon/ANI/repo/services/inference-service/internal/runtime/coresdk/adapter.go:358) | 保留租户/摘要/引用校验、PVC/对象存储差别，不复制旧物化协议 |
| 限流与并发 | [Redis limiter](/root/kubercon/ANI/repo/services/inference-service/internal/adapter/redis/rate_limiter.go:18)、[策略执行](/root/kubercon/ANI/repo/services/inference-service/internal/service/access_policy.go:120) | 删除旧 Redis bootstrap 不等于删除 QPS/RPM/max-inflight 功能；新数据面要落实等价行为 |

重要差异：旧 worker 在引擎 Health/Smoke 后设置操作完成并触发发布；新草案把 create/start 成功门槛延后到实际发布与带身份调用确认。这是明确的语义变更提案，不可称“完全沿用旧成功语义”；新契约冻结需同时评审客户端等待和超时。

## 检查结果

| 检查 | 结果 | 说明 |
|---|---|---|
| 固定来源 SHA/tree 和模板目录/装配 | pass | 实际 git rev-parse 与源码阅读 |
| 新文档本地链接、格式、来源路径 | pass | Node fs 只读检查 7 份 Markdown、38 个本地链接（其中 23 个源码行号）、13 个固定来源路径；issues=[]，exit 0 |
| 来源克隆未修改、Git tracked diff 空白检查 | pass | 两个来源 checkout status 为空且 HEAD 等于固定 SHA；git diff --check exit 0；新文档为未跟踪文件，另由上项检查覆盖 |
| 作者一致性复核 | pass | 区分确认/草案/未实现、无 CRD 方案、持久恢复、撤发布顺序及契约差异；非独立评审 |
| 独立阅读评审 | not_verified | 首次及缩小范围重试均因模型容量失败，未获得评审完成结论 |
| 新服务编译/单测/真实依赖 | not_verified | 尚未生成服务；本次新 migration 也未在真实 PostgreSQL replay |
| 公开产品调用、集群部署、切换 | not_verified | 本轮无此操作 |

工作区原有 `repo/frontends/console/.cache/`、`Dockerfile`、`src/` 未跟踪内容保留，未纳入本设计改动。网络读取首次遇沙箱 DNS 限制，之后只读升级权限访问成功；不把沙箱错误当成用户机器/集群故障。

独立仓库新增 `migrations/000001_inference_control_plane.sql` 和其 README。它是基于本规格的独立 schema 草案，不是从 ANI migration 复制；尚无 sqlc 查询或服务代码，因此不宣称 migration replay、约束负向测试或运行时验证通过。

检查过程异常：初版 Node 校验通过 child_process 调用 rg 被沙箱以 EPERM 拒绝，未视为检查成功；改为 Node fs 只读遍历和路径/行号校验后独立运行 exit 0。一次文档补丁上下文不匹配被整体拒绝，核对原文后重新 apply_patch 成功。上述问题均未引发服务或集群变更。
