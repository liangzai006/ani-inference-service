# 2026-09-12 continuation verification

本轮没有修改旧 ANI、前端、集群或远程仓库，也没有提交/推送。

## 已验证

- 开发机 PostgreSQL `recycling-postgres/ani_inference` 上，带
  `INFERENCE_PG_DSN` 的全仓 `go test -p 1 ./... -count=1` 通过；其中
  `internal/data/postgres` 真实集成测试通过。
- `GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...` 通过。
- `GOCACHE=/tmp/ani-go-cache go build -p 1 ./...` 通过。
- HTTPProbe adapter 的 2xx、非 2xx、传输失败和缺少 resolver 测试通过。
- 新增 endpoint 持久化和 typed Service apply/binding：要求显式 containerPort、Service port、targetPort
  和协议，并用 generation selector 隔离旧 runtime；Deployment/LWS 在提供端口时
  同样写入 typed `ContainerPort`。未猜测默认端口；Service 的真实 API 行为和
  publication/data-plane 地址确认仍未验证。
- 该切片通过定向 Kubernetes 测试、全仓 Go 测试、`go vet`、`go build` 以及本机
  PostgreSQL 集成包测试（3.706s）。
- `applied_generation` 的迁移、观测事务推进和 gRPC GET/LIST 投影已有真实
  PostgreSQL 证据。
- 用开发机构建的 `/tmp/ani-inference-service` 启动正式 Kratos 进程，访问
  `http://127.0.0.1:19091/healthz` 返回 `{"status":"ok"}`，随后由超时停止并
  观察到 HTTP/gRPC server 正常停止。该项只证明进程生命周期和健康入口，不证明
  Kubernetes worker 或业务 operation 已在正式进程中完成。
- 新增 PostgreSQL 只读 `StatusProjectionSource`、Controller status-only patch
  接线和 CRD status fake-client 测试；状态中的 `observedGeneration` 来自持久
  `applied_generation`，不使用 Kubernetes `metadata.generation`。
- 状态投影切片完成后，顺序执行的真实 PG 全量测试、`go vet`、`go build`、
  `sqlc generate`、`buf lint` 和 `buf generate` 均通过。
- 清理 Controller 中重复的 status projector 字段并重新顺序执行全量 Go 测试、
  `go vet` 和 `go build`，结果均为 `pass`。
- `served_model_name` proto/PG/读写投影、tenant-scoped `ListOperations` keyset 查询、
  Job wake-only watch 及 status stale-after 投影已完成；全量测试、vet、build、
  `sqlc generate` 和 `buf lint` 通过。
- 正式进程 `/metrics` 可返回 `ani_runtime_ready`；模型物化与 worker 指标在业务
  background composition 注入时使用低基数 OTel instruments。该检查不替代真实 provider
  指标端到端验证。

## 仍为 not_verified

- Service/endpoint 的真实 API、TLS、稳定名称、publication 地址和数据面确认合同。
- 真实 Core quota、Model/Storage、publication/data-plane、IAM provider。
- 真实引擎调用探针、LWS controller/GPU 多节点调度和正式进程 crash 恢复。
- CRD `status.observedGeneration` 与 service-level `applied_generation` 的最终
  对外语义。
- Job 若需要由 Inference 自己 apply/observe/bind，仍需冻结其 spec、migration kind
  约束和 runtime 状态语义；当前 Job watch 只承担唤醒。

在上述合同和依赖可用前，主进程保持 provider 缺失即未就绪/可重试，不创建伪造
账本或默认 endpoint。
