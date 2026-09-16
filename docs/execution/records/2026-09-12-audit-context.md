# 2026-09-12 异步审计上下文

## 决策

Inference 不在 `inference_operations` 复制调用者字段。受理事务已经把
`actor`、`request_id` 写入 `inference_audit_events`；worker 持有 resource-work
租约后，按 `tenant_id + operation_id` 读取最早的 `inference.*.accepted` 事件，
再把上下文带入每个 step/retry 审计事件。before/after 状态记录 operation 的
phase、step 和错误消息，且与 operation CAS/重试在同一 PostgreSQL 事务提交。

## 证据

- `internal/biz/inference/runner_test.go` 验证 transition/retry 事件保留
  Actor、RequestID 和 before/after 状态。
- `internal/data/postgres/operation_store_test.go` 在真实 PostgreSQL 中验证
  tenant+operation 查询恢复受理上下文。
- `queries/inference.sql` 的 `GetOperationAuditContext` 显式限定 tenant。
- gRPC transport 提供可选 `WithActor` context；IAM middleware 的正式可信主体
  合同尚未接入，不能把当前空值或测试值当成 IAM 验收通过。
- `internal/service/inference_test.go` 和 `command_test.go` 验证 Create、Update、
  Start/Stop/Restart/Delete 入口都会传递 Actor；本轮全量 Go 测试、vet、build
  和 gofmt 均通过。

## 限制

该方案只负责 Inference 领域审计事实；审计汇总、IAM 安全审计和跨服务事件仍归
对应公共能力。不能通过 GET/LIST 触发审计副作用。
