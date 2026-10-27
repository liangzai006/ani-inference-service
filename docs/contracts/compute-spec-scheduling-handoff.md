# 推理算力规格与集群调度接入 handoff

日期：2026-10-17。面向 Inference owner，依据容器实例联合批次及下列固定源码。
本文交接已完成能力、可复用经验和**尚待设计/实施的推理接入**；不是已上线接口合同，也不授权合并、部署或迁库。用户后续授权本 PR 同时移植前批保留的 GPU 数量补丁；新增算力规格与调度接入仍只交接方案。

## 1. 目标主链与当前断点

```text
平台经 Gov 管理 Res 数据库算力规格、Gov 调度策略
  → 租户经 Gov 选择规格、模型/引擎及可选调度意图
  → 当前身份/权限/套餐 → 原幂等记录检查
  → 仅新请求解析规格与当次默认值 → 冻结推理业务参数
  → GPU：ACC 完整 plan + 原 charges → Gov 一次持久占额/投递
  → Inference 原子受理 → PG 快照 → KServe → 实际 workload/Pod
  → 模型 RuntimeReady / Publication / 实际调用分别确认
  → 删除 → workload 清理 + 原 Pod UID 的 GPU 释放证据 → 原账累计退额
```

可观察结果必须包含：同 key 重放不会换规格/默认值或重复占额；实际 Pod 保留完整 GPU 硬约束并得到预期调度字段；删除按原 CREATE/charges 退额。Pod 创建、ACK、Running、模型就绪、发布和可调用是不同事实。

**当前断点：** 推理尚未接入本批 Res 规格和实例调度设置；上游基线 34c9fd9 的 GPU 数量仍要求 N=1 且 Profile.max=1，本 PR 迁入的数量增量将其改为按 Profile 上限校验。已有冻结 GPU 受理、持久化、KServe 渲染和删除释放源码可作为接入基础，不能据此宣称“选择规格 → 推理运行”已经打通。

## 2. 固定来源与完成状态

| 来源 | 固定提交/版本 | 本次核对的状态 |
|---|---|---|
| Inference 上游 | [`34c9fd9387eaee82de621ef18a3bf322dcd342e8`](https://github.com/liangzai006/ani-inference-service/commit/34c9fd9387eaee82de621ef18a3bf322dcd342e8) | 本文的当前源码基线；只读核对，没有重新运行或部署 |
| ACC | [`c1242866d9877d4fd5dee934ddc80e8ec8b23ad9`](https://github.com/zhangzhe-ctrl/ani-accelerator-service/pull/1)；`v0.0.0-20261016163330-c1242866d987` | 容器批次已提交、SSH 推送、正常模块下载核验；PR 打开、未合并 |
| Res | [`3b94192e16d9824ee6feaef23713cfcc299d062d`](https://github.com/zhangzhe-ctrl/ani-resource-service/pull/8)；`v0.0.0-20261016165835-3b94192e16d9` | 数据库规格、冻结、调度、诊断/日志源码已交付；PR 打开、未合并 |
| Gov | [`d3f618faade182436348ce183897abe05460dd71`](https://github.com/zhangzhe-ctrl/ani-governance/pull/23) | 固定上述 ACC/Res 正式模块；容器管理、授权、调度和 SSE 已交付；PR 打开、未合并 |
| 推理数量延期补丁 | fork [`eb6b4358f59b3a33f70d62f34a3627ab2688d307`](https://github.com/zhangzhe-ctrl/ani-inference-service/commit/eb6b4358f59b3a33f70d62f34a3627ab2688d307)，分支 `codex/instance-unified-20261016` | 基于旧 `79beaaf…` 的 13 文件补丁；已推 fork、历史定向验证通过，未进入上游/正式消费链 |

本 PR 在 34c9fd9 上以最小差异迁入上述数量改动和定向设施，保留上游单入口、删除执行及释放观察；不把旧 fork 整树覆盖到新基线。PR 待审不等于 canonical 模块已经发布或 Gov 已消费。

上表 PR 状态为本文编写时快照。ACC/Res 模块下载曾核对 `Origin.Hash` 和两项 checksum；有可下载版本不代表已合并或部署。Gov 仍消费原 Inference `v0.0.0-20261015020740-ceacc4063742`，没有消费 eb6 fork，也没有保留本批实验性公开推理数量字段。

### 容器批次已完成什么

| 能力 | 已完成及证据层次 | 推理接入时的边界 |
|---|---|---|
| 规格管理/目录 | Res PG CRUD/PATCH、行锁合并、启停用/删除、授权后分页计数；Gov 平台入口和租户安全投影；真实 HTTP 具名验证完成 | 当前 RPC/权限属于实例业务，不是现成的通用推理规格服务 |
| 冻结/幂等 | 旧 key 先重放，新 key 才读规格/默认值；真实 Gov PG + Res mTLS/PG 联合测试通过 | 推理需扩展自己的业务快照/hash，不能套 `container` canonical |
| CPU 主链 | Gov HTTP → PG → Notebook → StatefulSet → Pod Running/Ready；规格变更、旧快照、启停保盘及正常删除完成 | 不是 KServe/模型/Publication 的验收 |
| GPU 数量/原账 | ACC 正 uint32、N≤Profile.max，共享单卡；完整 plan/Runtime 与 checked R×N；较高上限及越界/溢出软件、PG 验证完成 | Capacity 对 N>1 仍为 `SHAPE_UNSUPPORTED`；数量支持不等于多卡容量估算 |
| GPU 控制器字段 | Whole1/Shared1/Whole2 实际 Pod 字段、软亲和/优先级、原 charge 清理核对；Pod 均 Pending | 未验证真实分配、隔离、绑定、物理释放或多卡模型 |
| 平台调度/CM | 条件写入、陈旧 RV 409、受保护摘要、默认值/租户名单和权限负向；已条件恢复测试配置 | CM 保存不证明调度器加载；现有 settings 只用于实例 |
| 诊断/日志 | 纯读 UID 归属链；Unicode、108.722 秒空闲 follow、600.066 秒期限、换 Pod、慢读者、断连和上游故障实测 | Inference 的 workload UID 链和容器目标需重新定义 |

以上现场来自任务自有旧 live binary；最终提交及新 PodGroup guard 另有软件验证，未部署最终版本。Res 新代码含 schema 3 前向迁移，现场仍 schema 2；将来升级须用最终生产 binary/SHA 和 Instance owner 显式迁移，不能改已应用 002 或让推理访问该库。浏览器、GPU 硬件和最终版本共享部署均未验。

容器正式接口和细节见 [Gov HTTP 合同](https://github.com/zhangzhe-ctrl/ani-governance/blob/d3f618faade182436348ce183897abe05460dd71/docs/contracts/container-instances.md)、[Res Proto](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/3b94192e16d9824ee6feaef23713cfcc299d062d/api/instance/v1/instance.proto)、[ACC 消费合同](https://github.com/zhangzhe-ctrl/ani-accelerator-service/blob/c1242866d9877d4fd5dee934ddc80e8ec8b23ad9/docs/contracts/accelerator-consumer-contract.md)。三份 PR 说明记录了具名验证及未验证边界；ACC 曾有独立供应链检查因许可证计数与 BOM 不一致失败，不能把 Fedora 验证通过当成 GitHub CI 全绿。

## 3. 算力规格：复用数据来源，补齐推理合同

### 现有 Res 接口到底能做什么

服务为 `/instance.v1.InstanceService/`，已有 `ListPresets`、`AdminListPresets`、`AdminGetPreset`、`AdminCreatePreset`、`AdminUpdatePreset`、`AdminSetPresetEnabled`、`AdminDeletePreset`。完整规格归 Res 的 `instance.compute_presets`，CPU/内存为精确整数 `cpu_millis`、`memory_mib`；GPU 引用包含 Profile ID/version/digest、每实例数量、cluster/pool、mode/model、共享 MiB 和 core 百分比。

`ListPresets` 的可信 `tenant_id` 与 `allowed_gpu_profiles` 由 Gov 构造。Gov 遍历 ACC 授权目录并核对 scope，Res 按 **Profile ID/version/digest/cluster/pool** 交集后过滤、计数和分页。公网不能提交该授权集合；GPU 目录拒绝/故障需明确表达，CPU 候选可保留，不能把残缺 GPU 集合伪装成完整目录。

**没有 `ResolveComputePreset` 或其他纯算力 resolver。** 现有 `ResolveCreateSpec` 接收 `tenant_id/data/namespace/scheduling_defaults`，解析镜像、workspace/PVC、SA、UID/GID 等，输出实例专用 `CreateBusiness/FrozenSpec`，GPU 固定 `replicas=1`、容器 `workspace`，业务限定 `instance_kind=container`。它不能直接给推理产生合法 business、owner attachment 或 plan。

Res 当前只接受登记的 Governance 身份和方法；平台/租户调用互斥。Inference 不能冒用 Gov 证书调用这些 RPC，也不能跨库读取规格或跨仓导入 `internal`。入口实现参考 Res `internal/service/instance/{presets,service}.go`、`internal/server/{governance,instance_identity}.go`，目录授权参考 Gov `instance_presets.go::allowedPresetGPU`。

### 建议的最小接入切片（未实施）

1. **Gov/Res/Inference owner 先固定字段及权限。** 为推理定义规格选择字段、查询范围和 compute-only 可信解析合同；接口名/字段号仍待确认，本文不虚构可调用的新 RPC。Res 继续拥有规格数据；Gov 负责用户授权、可信租户映射和目录交集；Inference 拥有推理快照/生命周期。优先 Gov → Res 解析、Gov → Inference 下发冻结结果，不额外开 Inference 直连 Res 的权限。
2. **定义与现有原始资源输入的关系。** 当前 Gov 推理 DTO 使用 CPU/内存、replicas 和四字段 GPU selection，没有规格/scheduling 输入。新增选择后必须明确请求是否允许资源覆盖及冲突处理，避免“选一份规格，再任意覆盖 GPU”。不复制实例旧字段清理、分页迁移或 `container` 类型到推理合同。
3. **先当前授权，再查原 key，最后才解析新值。** 原始请求 hash 保留用户明确选择/调度存在性；动态规格内容、默认值、ACC plan 不进入这个 hash。新请求的最终业务摘要覆盖完整实际 CPU/内存、GPU 请求、推理拓扑、模型/引擎及实际 scheduling。两层摘要不可混用。复用 Gov 受理的 resolver seam，不能在入口提前展开规格后再判断是否重放。
4. **以推理自己的消息持久化快照。** 保存规格 ID及名称/code快照、精确 CPU/内存、GPU 可信引用、原始调度与实际调度、完整 plan/原 charges。重试、恢复、执行和删除不读取当前规格/defaults；规格修改、禁用、删除不级联既有推理。不要给无版本的 Res 规格发明外层 `preset_version`；ACC `profile_version` 仍正常保留。
5. **沿现有 owner 事务扩展，而非新建旁路。** 推理 `managedCreate`、`prepareManagedGPU`、`CurrentRuntime` 已保存和验证冻结 GPU 数据。新增业务字段必须纳入 canonical/hash、Proto/JSONB 往返及最终渲染校验，显式用户值与冻结实际值冲突即拒绝。CPU 当前走 Gov direct trusted owner，GPU 走 Gov ledger；容器 CPU 的持久受理分支限定实例 owner，不能直接声称推理 CPU 已具备同一条占额路径。

CPU 始终不调用 ACC、不携带 GPU plan/charges，也不因本次接规格新增 CPU/内存配额。整数规格用 Kubernetes Quantity 精确转换，例如 `4000m`、`8192Mi`；不要经浮点或把 MiB 当字节/显存块。

## 4. 集群调度：全局配置、业务策略和 GPU Runtime 分开

| 层 | 责任与现状 | 推理 owner 的接入要求 |
|---|---|---|
| 平台调度器配置 | Gov `GET/PUT /api/v1/admin/scheduler-config`；固定 `volcano-system/ani-volcano-scheduler-configmap` 的 `volcano-scheduler.conf` | 沿用平台管理边界，不让 Inference 写 CM。保存用编辑时 resourceVersion；409 不取新 RV 强写；保留 metadata/其他键。调用 ACC `pkg/schedulerconfig` 的同一版本算法，不复制摘要实现/增加双读 |
| defaults / 租户可选 PriorityClass | 当前 Gov `instance.scheduling.<clusterUUID>`、`/api/v1/admin/instance-scheduling-settings`，使用 `instance:*` 专项权限 | 这是实例策略，推理尚未自动继承。Gov owner 需明确推理适用范围及权限；复用机制不等于直接套用 key/权限。无需新建优先级目录服务或租户默认值层 |
| 单次业务调度意图 | 当前实例 optional `pod_placement` / `priority_class_name`；显式值覆盖当次默认，无来源 placement=NONE | 推理需新增对应公开/内部/快照合同。原始缺省与显式 NONE 分开；JSON null、空对象/字段 null、UNSPECIFIED/未知值和空 class 在 ProtoJSON 丢失存在性前拒绝。配置读取失败不能静默采用空默认 |
| GPU 硬约束 | ACC 冻结 Runtime 的 scheduler、queue、node labels、annotations、runtime class、每容器 GPU limits | CPU/内存与业务调度放推理业务快照，不塞入 GPU plan；合并软亲和时不覆盖这些硬字段，不改原 plan/digest、不自造 GPU profile |

实际采用的 PriorityClass（含默认值）必须检查：当前用户权限、Gov 租户到资源租户的可信映射、本租户允许范围、目标集群存在性，以及当前 GPU/workload 路径能否保持同一 priority。校验失败在占额前拒绝；不创建 class，不修改已有 PodGroup，不把用户字符串直接当受信引用。

只使用已配置的目标集群，并核对规格、GPU request/plan 与该集群一致；不把容器临时实验配置复制成推理部署输入，也不因此新增集群选择体系。平台 CM 是集群级配置，但业务默认值适用范围仍需独立确认。

### KServe/Pod 的落点与陷阱

- 从已有 [`RenderKServeRuntime`](../../internal/data/kubernetes/kserve.go) 的冻结 GPU 投影扩展推理自己的调度映射。Deployment 场景的 GPU-bearing Pod 数为 R，卡数为每 Pod 的 N，容器为 `kserve-container`；不增加 Pod 代替卡数，不自动加 tensor/pipeline parallel 引擎参数。
- 软同机/分散只选择同租户、同推理服务及明确 generation 范围的 Pods；跨 generation 是否联动先固定语义。检查实际 labels 与 controller 行为，不照抄实例的 tenant+instance+namespace selector 或单 workspace 渲染器。保留已有 nodeAffinity/nodeSelector、资源及引擎参数；冲突不得静默覆盖。
- `priorityClassName`/affinity 等需在受支持的 KServe 输入投影后，读取控制器生成的 workload 和实际 Pods 证明传播；仅检查提交的 CR JSON 不够。GPU LWS 仍被拒绝；现有 C02 queue/template metadata 缺口和 typed projection helper 不代表已可用，本文不解禁 LWS。
- PodGroup 规范注解是 **`scheduling.k8s.io/group-name`**，queue 注解是 **`scheduling.volcano.sh/queue-name`**，两者不能混淆。实例现场 Group 与 Pod 同属 StatefulSet；推理必须按自身 controller/UID 归属链核实，不能假设 Group owner 一定是 Pod，也不能复制 Notebook→STS 链。以真实 API 键的字面 fixture 测试，避免生产常量写错却与测试一起通过。
- 若完整 Runtime 固定了既有 Group，只读核对其 namespace/name/priority；无相容路径则占额前拒绝。应用不抢写其他 controller 所有的 Group。诊断/事件/日志沿已保存 tenant/service/cluster/namespace 和 UID 链纯读，不调用会写 usage/签名的整个观察流程。

CM 的评分参数可变与受保护 GPU 配置由 ACC 单一算法区分；没有调度器加载确认或加载超时恢复功能。更改摘要基线需协调消费者，不伪造真实 Verified/usage。后续推理不需要为复用规格而修改这些全局职责。

## 5. 推理当前已有什么，延期补丁做了什么

### 上游 34c9fd9 已有源码（源码接线不等于现场验收）

| 路径/符号 | 当前职责 |
|---|---|
| [`internal/service/managed_gpu.go`](../../internal/service/managed_gpu.go)：`managedCreate` / `trustedGPUContext` | 验证 Gov 合同标记、tenant/actor、完整 plan、business/request hash、完整原 charge 向量与 GPU 子集 |
| [`internal/data/postgres/managed_gpu.go`](../../internal/data/postgres/managed_gpu.go)：`prepareManagedGPU` / `persistManagedGPUCommand` / `LoadRefundContext` | 原子保存原 CREATE、完整 plan/原账、命令/ACK 与重放快照；tenant+resource+原 CREATE 保持绑定 |
| 同文件 `acceptManagedGPUDelete` | 最新上游会为已存在服务保存 deleted 目标、delete operation/work；DELETE 先到仍保留 closing/tombstone 语义。不能沿用旧批次“只落 closing 意图”的描述 |
| [`internal/biz/inference/runner.go`](../../internal/biz/inference/runner.go)、[`internal/data/kubernetes/kserve.go`](../../internal/data/kubernetes/kserve.go) | 从受理快照取得 Runtime，经生产 renderer 投影；模型、运行、发布/调用状态仍分层观察 |
| [`internal/service/managed_gpu_release.go`](../../internal/service/managed_gpu_release.go)：`ManagedGPUReleaseCoordinator.ReleaseManagedGPU` | 核对原退款上下文、DELETE、runtime absence seal 和完整 Pod UID 范围，在本地生成确定性 evidence ID/digest 并落库，再报告 Gov；不调用 ACC |
| [`cmd/ani-inference-service/main.go`](../../cmd/ani-inference-service/main.go)、[`internal/data/postgres/operation_store.go`](../../internal/data/postgres/operation_store.go) | Gov reporter 配置时给 runner 接入 release coordinator；000019 迁移持久化释放证据所需 Pod UID 范围。源码接线不等于现场释放已验 |

当前只有普通业务 gRPC **19090**；TLS 1.3、入站唯一精确 DNS SAN `ani-governance`，可信 metadata 绑定 tenant/actor，GPU 附件 tenant 必须与上下文一致。不再使用独立 19092 或入站 SPIFFE URI。退款方向使用 Inference 自己登记的客户端 DNS 身份，账本 owner 仍为 `ani-inference`。详见[单入口设计](../superpowers/specs/2026-10-16-single-grpc-gpu-design.md)与[现有 GPU owner handoff](managed-gpu-owner-handoff.md)。管理 `/api/v1/...` 走 Gov；Higress 只负责 `/v1/...` 推理调用。

### eb6 数量补丁的成果与本 PR 移植范围

- 更新公开 `ValidateBusinessPayload`、内部 `gpu.ValidateRequest/ValidateManagedPlan`：整卡正 uint32、N≤Profile.max、Runtime number=N、checked R×N/原物理 charge；共享仍 N/max=1。不是按 1/2/3/4 分支，也不通过穷举证明通用化。
- 真实 ACC builder 的 `kserve-container` 向量包括同一 max2 Profile 的 N1/N2、Shared1，以及独立 max4/N4 和超限负向；完整 plan protobuf 转换、摘要/Runtime/totals 篡改校验、PG 往返/CurrentRuntime/生产 render/DELETE 原账上下文有历史定向证据。
- fork 正常 pin 新 ACC 模块，更新 `scripts/generate-api.sh` 的精确版本 assert，Go directive 为 1.26.9；正常模块生成及公开/内部/managed/adapter 受影响目标 exit0。此前 PG roundtrip 测试曾因 JSONB 排版被误作 wire canonical 失败，修正测试编码后目标通过；失败记录保留。
- **没有接入规格、scheduling、GPU LWS 或模型并行参数。** 未做推理全量、完整启动、真实多卡模型/硬件释放验收。较高数量的最后增量只补了软件目标，没有重跑新的 PG/现场矩阵；旧阶段证据不能提升为最新组合已验。
- 原基线是旧 79beaaf，本 PR 仅提取必要数量 delta、适配新基线并定向核对持久化/原账路径，保留当前 34c9fd9 的单入口和删除释放改进。没有迁入本批曾撤下的 Gov HTTP 入口，也不将代码移植视作正式模块发布。

Gov 容器 PR 保留原推理 BFF：GPU selection 仍只有 cluster/pool/profile ID/version，构造 N=1；实验性 `devices_per_replica` 入口已撤下。只更新 Inference 或 ACC 不会让用户通过当前 Gov 创建多卡推理；Gov adapter/公开输入、canonical、owner consumer 必须配套升级，未支持的数量应在占额前拒绝。

原退款身份始终是 **原 CREATE operation + 完整原 charges + 持久化上下文 + 稳定 release_event_id + 累计 released_total**。当前 coordinator 用 DELETE ID 确定性派生 event ID；重试复用事件，不按当前规格、存活 Pod 数、显存块数或新 Profile 重算额度。DELETE ACK、对象暂时缺失或 usage=ENDED 都不替代完整清理和 ACC 释放证据。

### 本 PR 移植后的定向验证

Fedora、Go 1.26.9、既有 heavy 锁及 2 CPU/6 GiB 限制，`GOWORK=off`、正常模块、无 replace/modfile。当前输入移植自 eb6，PG quantity 用例额外断言新版 DELETE 产生 `pending/withdraw_publication`、target generation=2；没有改这些上游生产路径。

| 检查 | 本次实际结果 |
|---|---|
| 7 个改动 Go 文件 `gofmt -l`；正式 API lint/generate/生成漂移检查 | exit0；没有重写生成物 |
| ACC 模块下载/选中来源；`go mod verify`；`go mod tidy -diff` | exit0；Origin.Hash=c1242866…，双 checksum 与已交付版本一致 |
| 公开、biz/gpu、adapter 数量定向 | exit0，分别 0.006/0.012/0.007s，无 skip；含 Whole1/Whole2/Shared1/WholeProfileMax、完整转换及重算摘要后的语义负向 |
| `TestManagedGPUReleaseRequiresAcceleratorEvidenceBeforeGovernance` | exit0，service 包 0.006s；释放证据前不报告 Gov 的软件边界 |
| `go test ./cmd/ani-inference-service -run '^$'` | exit0，0.032s；只证明装配编译，明确没有执行测试或启动服务 |
| PG `TestPostgresManagedGPUQuantityPlanRoundtrip`、`QuantityRejectsWrongOriginalCharge`、`DeletePreservesAcceptedCreateAndRejectsTopologyMutation` | exit0，包 0.382s，无 skip；Whole1/Whole2/Shared1 完整 plan/CurrentRuntime/生产 render、原账/跨租户/DELETE 执行断言 |

PG 仅使用新建任务独占库 `cf_01a14526_inf_handoff_tests`，显式事务应用当前 000001..000019；owner/runtime 分离，未迁移或更改既有四库。较高 max4/N4 此次仍是软件向量验证，不声称新 PG/现场多卡矩阵或物理释放。

首轮停在 tidy-diff：go.sum 残留旧 ACC 的两行 checksum。同步删除这两行后从只读 tidy/mod-verify 继续，后续目标全部通过；原 `check01/run.exit=1` 与 diff 保留，`check02/run.exit=0`。已通过的生成未重跑。没有全量测试、完整服务启动、集群操作或新硬件验收；外部释放观察在单元测试边界替代。

## 6. 建议 owner 的落地顺序与必要验收

1. 固定本文基线与 Gov/Res 接口决策：纯规格解析/授权、推理字段/快照、原始 hash 与冻结 digest、调度策略适用范围、CPU direct 与 GPU ledger 的受理职责。
2. 从 **CPU Deployment 的最小主链** 开始：真实 Res 规格 → Gov 授权及原 key → Inference PG 冻结 → KServe 实际 Pod；先证明规格/defaults 变化后旧 key 与旧 generation 不漂移，新 key 用新值。模型可调用仍按原 Model/Engine/Publication 合同另验。
3. 完成数量合同的正式发布与 Gov 配套：本 PR 已迁入 Inference 数量侧。正式组合仍核对完整 plan 的 protobuf/JSONB 往返、R 与 N 独立、Shared1、Profile 上限/上限+1、0/非整数输入、checked 乘积/溢出、摘要正确但语义冲突、全部原 charges。只覆盖规则和边界，不新增整数穷举或多卡现场大矩阵。
4. 接实际 scheduling：原始 presence、默认快照、显式 NONE、PriorityClass 名单/存在性/路径相容、读取失败；平台/租户互斥、跨租户/伪身份/伪内部字段拒绝。实际 KServe workload/Pod 核对软规则与所有 GPU 硬字段，分别记录 Pending、Running、RuntimeReady、调用结果。
5. 在最新上游 delete/release 代码上验证原账：重复 CREATE/DELETE、DELETE 先到、旧 key 撤权、Pod UID 释放范围保存、ACC 未释放/故障时不退、释放证据后原额退回、重复通知不重复退。仅边界系统不可用时使用明确替身，不能模拟掉受理、身份、事务、存储或业务规则。

复用当前具名设施：[managed PG](../../internal/data/postgres/managed_gpu_test.go)、[KServe renderer/控制器字段](../../internal/data/kubernetes/kserve_test.go)、[GPU 业务校验](../../internal/biz/gpu/gpu_test.go)。本 PR 同时迁入 [quantity PG](../../internal/data/postgres/quantity_test.go)、[数量校验](../../internal/biz/gpu/quantity_test.go)和 [builder 向量](../../testdata/accelerator-quantity-plans.json)；上游 34c9fd9 基线本身没有这些新增文件，结果须对应具体源版本。

生成、构建、测试、SQL 和集群工作沿既有 Fedora 独占环境及 owner/runtime 库权限；缺必需环境明确失败或 NOT_RUN。默认只跑改动相关目标，使用真实构造和业务行为；不运行推理全量或完整拉起服务来替代定向验证。文档修改只做引用/一致性检查，不自动触发软件重验。

### 正式模块交付与恢复

模块仍为 `github.com/liangzai006/ani-inference-service`。旧 eb6 只在 fork，曾因 canonical 无写权限无法按该来源下载，用户因此延期；不要用长期 replace/go.work、伪版本或模块改名绕过。通过 fork 提供评审差异，由有权限 owner 处理 canonical 实现及发布，之后正常 `GOWORK=off` 下载核对 `Origin.Hash`、checksum，再由 Gov 固定真实版本。本 PR 按新增授权纳入数量代码与必要定向验证，尚未完成 canonical 发布、Gov 消费或新规格/调度接入。

提供方顺序是 ACC → 必要 Res 合同 / Inference 合同 → Gov；只升级实际消费的模块，Proto/生成与 `generate-api.sh` assert 同步。已有数据迁移保持不可变；新快照字段若需迁移，由 Inference 自己显式迁移并制定回退/前滚边界，不执行 Res 迁移或读取别人的业务表。

本批原始恢复索引在任务独占目录 `/home/chabking/.codex/tasks/instance-unified-20261016-01a14526/evidence/`：`DELIVERY.md`、`FORMAL-MODULES.json`、`CF-03-INFERENCE.md`、`UA-ACCEPTANCE.md`。这些是工作机材料，不是 PR 可下载附件；可审的固定源码、具名设施与结果摘要已在本文和三个容器 PR 提供。原失败保留，硬件、浏览器、共享部署和推理新规格/调度端到端保持未验证，完成相应验收后再更新结论。
