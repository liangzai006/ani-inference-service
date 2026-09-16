# 2026-09-12 发布后健康与 LWS 纠错

## 缺陷与修复

1. 原 worker 就绪计数接受任意非空 worker-index，把 LWS leader（官方 index 0）
   也算作 worker，重复位置和正在终止的 Pod 可以掩盖缺失 worker。现在只统计
   当前 generation、有效 group/worker 位置的 Running/Ready 且未终止 Pod，
   对位置去重。测试先复现 leader、多余/重复/无效位置、非 Running/终止 Pod
   引起的误判，然后验证修复。
2. endpoint Service 原名与 LWS 自建的 headless Service 同名，selector 也会选中
   worker。现在 endpoint 使用 `<runtime-name>-endpoint`，通过 Kubernetes 名称
   校验，LWS selector 添加官方 `worker-index=0`。未接入持久 spec 或真实集群。
3. 原 `observe_runtime` 在 `publish` 前要求 published endpoint 健康，形成循环
   依赖；原发布确认后直接成功。现在 create/start/update/restart 都经过持久
   `publish → verify_invocation → complete`。验证步骤要求当前 tenant/service/
   generation 的已确认 publication，以及 runtime、模型和调用健康事实。
4. terminal operation 的 Ensure 原来只观察 Kubernetes，未继续探测调用地址；
   现在与 operation 共用健康观察，只有当前代 publication 的 desired/observed
   均 published 才发起探测。未知/探测错误可落库，不用历史健康值冒充新观测。
5. PG 原合并逻辑把显式 unknown 替换为旧 healthy。现在只有未提供字段（空值）
   才可保留同代事实；显式 unknown/不健康会更新，跨代不继承。

## 验证范围

- 源码回归：LWS worker 计数、Service leader selector/独立名称、发布前跳过
  invocation probe、发布后验证、持续观察健康/不健康/未知/探测错误。
- 真实 PG：000011 仅扩展 operation step CHECK，以单事务应用；从唯一迁移源
  执行 sqlc generate。覆盖同代/跨代/跨租户 health 合并、RuntimeSource publication
  generation fencing、create/update/restart/stop/start/delete 生命周期。
- 故障注入：在 verify_invocation 事实保存后、operation terminal CAS 前注入
  中断，重建 worker 从原步骤重试；这不是正式进程被终止后重启的证据。

真实 authority、数据面鉴权/探测协议、引擎加载、LWS controller/GPU 调度及正式
进程 crash 恢复仍为 `not_verified`。本记录不证明实际发布或模型可调用。

## 本轮结果

- `INFERENCE_PG_DSN` 指向本机独立数据库的 `go test -p 1 ./... -count=1`：
  `pass`，PostgreSQL 包 4.119s，包含新生命周期与 observation 回归。
- `GOCACHE=/tmp/ani-go-cache go vet -p 1 ./...`：`pass`。
- `GOCACHE=/tmp/ani-go-cache go build -p 1 ./...`：`pass`。
- `sqlc generate`：`pass`；没有增加依赖或重复 DDL 来源。

## Endpoint 持久化与 Service apply 补充

本轮将 endpoint 合同接入 gRPC `RuntimeSpec.Endpoint`、typed CRD、`inference_specs` 的 nullable 列和 sqlc 查询；省略 endpoint 时四列保持 NULL，服务不会创建 Service，禁止推断 8080。显式 endpoint 的 targetPort 同时支持数字和命名端口，协议只接受 TCP/UDP/SCTP。

RuntimeExecutor 在 runtime apply 后用同一代 generation/UID/resourceVersion binding apply Inference-owned `Service`（名称 `<runtime>-endpoint`）；LWS 模式 selector 只选 leader，避免误选 LWS controller 的 discovery Service。删除和 absence observation 会包含 endpoint binding。真实 PG 的 000012 migration、Create integration、全量 Go test、vet、build 和 Kubernetes focused tests 已通过；真实集群 Service 行为和数据面 publication 仍为 `not_verified`。
