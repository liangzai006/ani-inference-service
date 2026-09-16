# 独立 Inference 服务设计入口

这是独立 `ani-inference-service` 仓库的工程与设计入口，按用户提供的领域准则组织。当前实现仍是分阶段切片，也未修改集群。当前实际进度只看[执行状态](execution/status.md)。

## 阅读顺序

1. [领域词汇](../CONTEXT.md)：概念及关系。
2. [行为与工程规格](specs/inference-service.md)：职责、结构、接口草案、持久化、生命周期与验收。
3. [Controller 与事实来源选型](adr/0001-controller-and-authority.md)：为什么采用 Kratos + controller-runtime + client-go，以及是否需要 CRD。
4. [分阶段交付计划](plans/inference-service.md)：依赖、允许范围和交接边界。
5. [执行状态](execution/status.md)：现在实际完成了什么。
6. [持续目标](execution/GOAL.md)：重构循环、30 秒中断重试和当前执行顺序。
7. [固定来源与检查记录](execution/records/2026-09-10-design-baseline.md)：来源 commit/tree、旧实现依据和本次验证层次。

规格标识：`确认` 指用户已经明确的方向；`草案` 指待评审的具体设计。文档存在不代表实现、测试或部署通过；实际证据只看执行状态。
