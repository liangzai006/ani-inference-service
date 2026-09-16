# ADR 0002: Inference Service endpoint contract

状态：accepted for internal implementation（外部消费者和数据面合同仍待确认）

## 背景

Inference 使用显式 endpoint 的 typed `corev1.Service` renderer；gRPC、CRD、
PostgreSQL `inference_specs`、`RuntimeSource` 和 Service binding 已接通。publication
的 `invocation_url` 是数据面事实，不能反向充当 Service spec。

## 提案

内部 endpoint 合同已同步进入 gRPC、CRD、PG spec、RuntimeSource 和 runtime binding：

- `container_port`、`service_port`、`target_port`、`protocol` 必须显式提供；
- `target_port` 允许数字或命名端口；
- Service 使用 `<runtime-name>-endpoint` 稳定名称，selector 包含 generation；
- Service 非 headless，ClusterIP 和删除权归 Inference；LWS 自有 discovery Service 不被接管；
- TLS/transport/auth/health path 的所有者和字段语义仍待数据面确认；
- 端口变化通过新的业务 generation 生效。

## 不变约束

- 不从镜像、argv 或环境变量推断端口，不隐式选择 8080；
- Service 存在不等于 publication 生效，publication 仍由其 authority 确认；
- Service binding 必须持久化 UID/resourceVersion，并使用 generation/lease fencing；
- endpoint 的核心查询字段进入正式列和 sqlc 查询，不藏进 JSONB；
- 内部 endpoint 字段与 Service apply 可以在已授权重构范围内继续实现；公开
  publication 接线仍需真实数据面协议，不能伪造 provider 或 invocation URL。

## 后续实施顺序

已完成 proto/CRD/migration/sqlc/clone/hash/read projection、typed Service SSA、
readback binding 和 observe/delete；下一步是接入 publication provider，
并用 envtest、真实 PostgreSQL 和真实数据面依赖分别验证。
