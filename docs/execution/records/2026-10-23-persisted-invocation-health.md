# 持久化推理调用健康观测

日期：2026-10-23。目标是补齐推理服务列表/详情长期显示 `health=unknown` 的缺口。

## 原因

原有 `/api/v1/inference/services/{id}/runtime` 会直连当前运行 Pod 的
`/v1/completions`，但结果只返回给这一次诊断请求；KServe 的持续运行观测把
`invocation_health` 固定保存为 `unknown`。因此一次直连成功不会改变服务列表和详情
中的健康值。初始化阶段也没有把“尚未启动”与“无法判断”分开。

## 实现

Deployment 和 KServe 两条运行时路径共用已有的 Pod 归属校验与最小调用探测。持久化
观测只在以下条件同时满足时执行：

- 当前业务 generation 的 runtime phase 为 `ready`；
- 该 generation 已经发布；
- 模型就绪事实已确认。

探测结果 `healthy`、`unhealthy` 或 `unknown` 通过现有 runtime observation 写回，继续
受 tenant、service、generation 和 work lease 的 CAS 保护。探测不可用或没有可确认的
结果时保留 `unknown`，并记录 `invocation_probe_unavailable` 原因；服务尚未 ready、
没有发布 endpoint、没有 ready Pod 时显示 `unhealthy`，原因字段说明是
`runtime_not_ready` 或具体探测失败；Pod readiness 仍不会直接冒充调用健康。

状态投影采用以下初始化语义：runtime 使用 `pending`，模型已物化但 Pod/KServe 仍在
启动时使用 `loading`，没有 Higress 路由时使用 `withdrawn`。`unknown` 只表示观测源
确实不可用或探测期间发生代际变化，保留它是为了区别“已确认不健康”。

这条链路只证明模型运行时能够被调用，不代表 Higress 鉴权、路由策略或业务指标统计。
Higress 仍只承载 OpenAI 兼容的 `/v1/...` 数据面，治理 API 继续经 Governance BFF。

## 真实验证

在 `ani-system` 通过 Governance BFF 创建临时 CPU vLLM 服务，使用已存在的 Qwen2
模型版本和 `cpu-4c-8g` preset。创建 operation 最终为 `succeeded`，服务经历
`ready/published`，随后服务详情中的 `health` 从初始 `unknown` 收敛为 `healthy`，
`observed_at` 持续更新。第一次模型启动尚未能调用时曾记录 `unhealthy`，模型完全就绪
后自动恢复为 `healthy`，证明写回的是调用结果而不是静态 ready 状态。

验证服务、KServe 对象、预测 Pod、下载 Job/PVC 和临时 Governance 用户/角色均已删除；
服务列表恢复为空。没有提交或推送本地源码变更，部署验证使用了带健康观测代码的临时
镜像 `docker.changqingyun.cn/ani/inference-service:health-observer-20261023`。
