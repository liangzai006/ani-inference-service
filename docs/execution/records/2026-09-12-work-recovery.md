# Durable work recovery evidence

## Scope

验证进程重启后，PostgreSQL 中仍保留 `inference_services`，但 `inference_resource_work` 行缺失时，启动恢复会补回持久工作项；同时验证租约过期后旧 worker 不能提交、后续 worker 可以重新领取。

## Evidence

使用开发机本地 PostgreSQL：

```text
INFERENCE_PG_DSN=postgres://recycling_user:recycling_pass@localhost:5432/ani_inference?sslmode=disable \
  go test -v -p 1 ./internal/data/postgres \
  -run 'TestWorkStore(LeaseAndObservation|Recover)' -count=1
```

结果：

```text
TestWorkStoreLeaseAndObservationIntegration PASS
TestWorkStoreRecoverRecreatesMissingWorkIntegration PASS
ok .../internal/data/postgres 0.508s
```

## 结论

本地 PostgreSQL 证据为 `pass`：`Recover` 通过持久 service 聚合补建 `resource_work`，`Claim` 按租约和 generation 重新领取。真实 Kubernetes API、LWS、模型制品、配额服务、发布数据面和引擎调用仍为 `not_verified`。

## Worker 生命周期补充

此前 `Recover` 只由主进程构造 Kubernetes background server 时调用一次；运行
期间丢失 `resource_work` 行时，单靠 Watch 或 due scan 无法重新发现该服务。
现已将恢复函数作为 `work.Loop` 的可选能力：启动首轮扫描前执行一次，之后按
`RecoveryInterval`（默认一分钟）运行 `Repair`，只补缺失的 work 行并保留已有
通知、租约和重试时间；没有轻量 Repair 能力时才回退到完整 Recover。恢复失败
只记录错误并继续处理已有 durable work，下一恢复周期重试；不会建立内存队列。

定向验证：

```text
GOCACHE=/tmp/ani-go-cache go test ./internal/biz/work ./internal/server -count=1
INFERENCE_PG_DSN=postgres://recycling_user:recycling_pass@localhost:5432/ani_inference?sslmode=disable \
  GOCACHE=/tmp/ani-go-cache go test -p 1 ./... -count=1
GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...
GOCACHE=/tmp/ani-go-cache go build -p 1 ./...
```

结果：上述测试、`vet` 和 `build` 均 `pass`。该证据证明恢复调度和本地
PostgreSQL 集成路径，不等同于真实进程崩溃、Kubernetes/LWS 或外部 provider
恢复已验证。

## 周期修复与启动唤醒分离

完整恢复若在每个周期都复用 `Notify`，会递增已有 `dirty_version`，把所有服务
反复变成“立即到期”。因此 `WorkStore.Repair` 使用 sqlc 查询
`EnsureResourceWork` 的 `ON CONFLICT DO NOTHING` 语义：只补建缺失行，已有行的
通知序列和下一次观察时间保持不变。`Loop` 首轮调用 `Recover`，后续周期优先调用
`Repair`；未提供 `Repair` 的抽象 store 才回退到原有 `Recover`。

真实 PG 的 `TestWorkStoreRepairDoesNotWakeExistingWorkIntegration` 验证已有
`dirty_version=9` 在周期修复后仍为 9；缺失行恢复测试仍为 `PASS`。
