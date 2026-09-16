# ani-inference-service

独立 Inference 服务仓库。当前包含 Kratos gRPC/layout 骨架、独立 PostgreSQL 持久化、typed CRD/Controller 适配和分阶段设计；不依赖 ANI monorepo 运行时。

完整文档入口：[docs/START-HERE.md](docs/START-HERE.md)。当前状态：[docs/execution/status.md](docs/execution/status.md)。

## 文档

设计导航、规格、ADR、计划和执行记录均位于 `docs/`；领域词汇位于仓库根目录 `CONTEXT.md`。

规格标识：`确认` 指已明确方向；`草案` 指待评审设计。文档存在不代表实现、测试或部署通过；实际状态只看 `docs/execution/status.md`。
