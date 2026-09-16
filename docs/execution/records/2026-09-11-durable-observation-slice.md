# Durable work 与 runtime observation 切片

日期：2026-09-11

## 已完成

- `queries/work.sql` 增加 `ListDueResourceWork` 和 `ListUndeletedServices`。查询按租户、删除状态、lease 到期、`next_run_at` 和 dirty/ack 版本筛选；扫描结果只是快照，执行仍必须重新 `Claim`。
- `WorkStore.ScanDue` 和 `ScanUndeletedServices` 提供启动恢复与周期扫描入口，真实 PostgreSQL 测试覆盖两个租户互不读取。
- `queries/inference.sql` 的 runtime 观察读写包含 `runtime_mode`、`ready_groups`、`ready_workers`、`lws_uid`。`UpdateObservationCAS` 现在要求目标 generation 等于当前 service desired generation，并返回受影响行数；旧 generation、未来 generation 和 desired generation 已变化的结果都会被拒绝。
- controller-runtime runtime event 映射改为按 tenant/service label 查询 CR，Pod 的随机名称不会丢失唤醒；这只是事件映射，尚未替代持久 runtime binding 的 UID/resourceVersion 核验。
- `internal/biz/work/Loop` 提供跨租户发现后的周期 Claim 调度；它不缓存业务任务，错误在下一周期重试。当前只完成可复用调度边界，尚未接入进程生命周期和真实 executor。
- 生命周期命令已增加独立的 PostgreSQL 受理事务：Start/Stop/Restart/Delete 使用 expected generation 锁定服务、复制配置 generation、写入 operation/resource_work/idempotency/audit，并返回异步 operation；gRPC adapter 将租户、幂等、CAS 和领域错误映射到固定状态码。
- `000003_inference_bindings_publications.sql` 已在本机 PG 增加 `inference_runtime_bindings`、`inference_publications` 和 `inference_observation_events`，用于 UID/resourceVersion 绑定、发布确认及不可变观察历史；三张表均无 RLS，且通过 tenant/service 复合外键归属 Inference。
- Read 列表已使用 tenant-scoped `(created_at, id)` keyset 分页，签名 token 绑定租户和游标；真实 PG 测试覆盖三页无重复、末页结束和跨租户隔离。
- Update 已使用不可变 spec generation clone：在 expected generation 下替换 Kubernetes resources、replicas 和 runtime mode/worker 数，写入 operation、resource_work、幂等和审计；真实 PG 测试确认 generation=2 新 spec 和旧 generation 拒绝。

## 验证证据

- 开发机真实 PostgreSQL：durable work due/startup scan、跨租户隔离、LWS observation 字段和 generation CAS 集成测试通过。
- `go test -p 1 -mod=readonly ./internal/data/postgres ./internal/data/kubernetes ./internal/service ./internal/biz/...` 通过。
- `go build -p 1 ./...`、`go vet -p 1 ./...` 通过。

## 仍未完成

这些变化还没有把扫描接到进程启动、controller-runtime manager 或实际 runtime executor；LWS/Deployment 的 UID/resourceVersion binding、模型就绪、publication、配额 Reserve/Confirm/Release、List/Update RPC 仍为 `not_verified`/未实现。
