# 2026-09-12 durable composition and observation slice

## 已验证

- `main.go` 在显式 `ANI_KUBERNETES_ENABLED=true` 且有 `ANI_DATABASE_DSN` 时创建 PostgreSQL WorkStore、durable recovery、controller-runtime manager、typed CR/runtime executor、lease-scoped operation runner/terminal reconciler、LoopServer 和 ManagerServer；未启用时不隐式访问 Kubernetes。
- worker 使用 `Reconciler.Execute`，必须持有 PostgreSQL `resource_work` lease token；Current、runtime 执行和 observation 写回均不能由 CR Watch 直接绕过租约。
- Controller-runtime CR/runtime Watch 只通过 WorkNotifier 写入 PostgreSQL wake-up；Reconcile 不直接执行 runtime，也不把 Kubernetes 事件当持久任务。
- `RuntimeExecutor` 使用 typed CR/Deployment/LWS SSA，严格校验 ownership、generation、UID/resourceVersion 和 Deployment/LWS readiness；runtime 退化保持可见。
- observation 写回使用 tenant/service/generation/lease CAS，并在同一 PostgreSQL 事务中更新 runtime projection 与 runtime binding CAS；旧对象消失后才删除 binding。
- 新增 operation `delete_runtime` 和 `observe_absence`，并用 `000004_operation_runtime_steps.sql` 持久化撤发布→删除→确认消失→释放配额的步骤词汇。
- 新增显式 operation runner 与 PostgreSQL OperationStore；每步通过 quota/publication/runtime/admission port 执行，CAS/retry 同时受 resource_work lease 保护，CAS 与审计可在同一事务提交。

## 验证

- `sqlc generate`：pass
- `GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1`：pass（开发机）
- `GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...`：pass
- `GOCACHE=/tmp/ani-go-cache go build -p 1 ./...`：pass
- 本机 PostgreSQL：operation CAS/retry、lease-fenced observation、runtime binding CAS、migration `000004`：pass

## 尚未完成

Quota authority adapter、publication adapter、真实 admission/engine health provider 仍未接入主进程；runner/store 和 RuntimePort 已接线，缺失 provider 时按 durable retry 安全失败。当前 runtime source 在没有确认事实时保持安全拒绝。真实 Kubernetes/LWS/GPU/引擎/IAM/Model 链路仍为 `not_verified`。
