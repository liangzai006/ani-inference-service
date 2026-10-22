# Inference AK 迁移进度记录

日期：2026-10-22

分支：`codex/inference-ak-api-20261022`

## 已完成

- 已冻结本轮边界：只在 Inference 内部增加 AK 签发、元数据管理和鉴权校验，暂不修改 Governance BFF；`/api/v1` 继续由 Governance BFF 提供，Higress 只负责 `/v1/...` 推理入口。
- 已采用 opaque `x-api-key` 合同。Inference key 与 Governance 平台 key 使用独立表、独立前缀和独立校验端点，不复制平台密钥。
- API 子模块已生成并发布，提交为 `1f0ae265510f`；根模块已升级到该伪版本，没有使用本地 `replace` 或 `go.work` 绕过模块合同。
- 已增加 `InferenceAccessKeyService`：创建、列表、查询、撤销、轮换和 `CheckInferenceAccess`。管理 RPC 只允许 Governance mTLS 身份；校验 RPC 只允许 `ani-inference-gateway.ani.internal`，且禁止携带租户或操作者 metadata。
- 已增加 Inference domain service：CSPRNG 生成 `iak_` 密钥，Argon2id 哈希存储，一次性返回 secret，支持 scope、过期、撤销、轮换、创建/轮换幂等和 key 级 QPS/RPM/小时限流。
- 已增加 PostgreSQL migration `000022`、sqlc 查询/生成代码、审计事件和原子 rate bucket；轮换使用行锁和部分唯一索引保证并发幂等。
- 已移除运行时 Higress Console 客户端和凭据依赖，bootstrap/model 路由继续走 Kubernetes HTTPRoute；bootstrap 路由增加 owner label，发现外部 owner 或删除中的对象时拒绝接管。
- 已补充 Helm/chart 校验、HTTPRoute owner 测试、domain/transport/storage 测试和 PostgreSQL 集成测试。
- 已实现独立 `ani-inference-gateway-authz` HTTP adapter：Higress 官方 `ext-auth`
  只转发必要请求头，adapter 通过 `ani-inference-gateway.ani.internal` mTLS 调用
  `CheckInferenceAccess`；Inference 或 adapter 不可用时 fail-close 为 503，决策映射
  为 401/403/429。当前网关切片只使用 model-router 生成的模型头，避免把可能晚于
  AUTHN 的 HTTPRoute service ID header 当作可信身份；模型 HTTPRoute 只在上游移除
  `x-api-key`。
- 已补充 `Dockerfile.authz`、可选 `gatewayAuthz` companion container/Service/客户端
  Certificate、ext-auth WasmPlugin 配置和 adapter/route 单测；默认不启用 companion，
  需要先发布 adapter 镜像再应用插件。ext-auth 使用 `/v1/` 黑名单路径，只保护
  OpenAI 兼容入口，避免拦截 Governance `/api/v1/`。

## 已验证

本地隔离 PostgreSQL 数据库 `ani_inference_ak_verify` 已执行仓库全部 migration，且三张 AK 表创建成功。使用 `INFERENCE_PG_DSN` 运行的真实集成测试通过，覆盖：

- secret 只在创建/轮换成功响应中出现，数据库只保存 Argon2id 哈希；
- 创建幂等重放不返回新 secret；
- 跨租户查询隔离；
- service/model scope 拒绝；
- 并发轮换同一幂等键只产生一个有效轮换，另一请求安全重放；
- 旧 secret 在轮换后失效，撤销后立即拒绝；
- RPM 原子 bucket 返回限流和重试时间；
- 审计事件和 rate bucket 持久化。

已通过的代码级检查：

```text
go test ./...
go test ./internal/data/postgres -run TestInferenceAccessKeyRepositoryIntegration -count=3
scripts/generate-api.sh check
scripts/verify-api.sh
git diff --check
```

本次恢复后又通过了 `go test -p 1 ./... -count=1`、`go vet -p 1 ./...`、
`go build -p 1 ./...`、`GOWORK=off ./scripts/generate-api.sh check`、
`GOWORK=off ./scripts/verify-api.sh`、`bash charts/verify.sh`、
`bash scripts/preflight_test.sh` 和 `git diff --check`。期间发现根迁移说明与
Chart 打包副本未同步，已补齐 `charts/ani-inference/migrations/README.md` 后重跑
Helm 校验通过。当前环境未提供 `INFERENCE_PG_DSN` 或 `ANI_DATABASE_DSN`，没有把
本次恢复误报为新的真实 PostgreSQL 集成运行。

本次 authz adapter 继续验证通过：

```text
go test ./internal/data/kubernetes ./internal/service/gatewayauthz ./internal/data/inferenceaccess ./cmd/ani-inference-gateway-authz
bash charts/verify.sh
```

全量 Go test、vet、build 和 API 校验也已重新通过；Helm 校验同时渲染了
`gatewayAuthz.enabled=true` 的 companion/Service/证书路径。当前仍未连接目标集群，
因此不能把 adapter 代码和清单验证写成真实 `/v1` 动态 AK 已生效。

集成测试需要通过环境变量提供本地 PostgreSQL DSN；凭据不写入仓库、文档或测试输出。

## 暂未完成

- Higress authz adapter 代码和 opt-in 部署投影已完成，但尚未在真实 `/v1` 流量启用；
  当前不能宣称动态 AK 已在网关生效。
- 租户聚合限流、模型全局限流、Redis/cluster limiter 和跨网关一致性属于后续切片。
- 本次 adapter、路径边界和安全默认收敛提交为本地 `11110a1`
  (`feat: add fail-closed inference gateway authz adapter`)；按当前任务要求尚未推送。
- 本轮根模块改动已提交并推送：`a0f468d` (`feat: add inference access key service and gateway bootstrap`)；
  API 子模块提交 `1f0ae265510f` 仍已发布。
- Inference 分支尚未合并到 `main`；当前 PR 为
  [#7](https://github.com/liangzai006/ani-inference-service/pull/7)，以 `a0f468d`
  为实现基线，并保留下方 Higress authz adapter 的未完成边界。

## 恢复入口

继续工作时先阅读本文和
[`docs/design/ai-gateway-auth-and-rate-limit.md`](../../design/ai-gateway-auth-and-rate-limit.md)，
然后在 `codex/inference-ak-api-20261022` 分支检查工作树。不要修改 Governance BFF、
不要把 secret 写入 Kubernetes 对象，也不要把 `/api/v1` 接入 Higress。
