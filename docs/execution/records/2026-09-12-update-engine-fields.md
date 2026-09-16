# 2026-09-12：Update 持久化引擎与制品字段

Update 链路现在接收并克隆 `ModelArtifact` 和 `EngineSpec` 的可选变更。非空制品引用、镜像、引擎类型和 argv 会写入新 generation；未提供时保留上一代值，避免普通资源更新清空运行配置。`command` 与 `args` 在服务层合并为 argv，不经过 shell。

验证：Buf 生成/lint、sqlc、全量 Go 测试、vet、build 和设置 `INFERENCE_PG_DSN` 的 PostgreSQL 集成测试均通过。真实 Model 制品物化和引擎健康仍未接入，因此仍不能标记 runtime 产品链路完成。
