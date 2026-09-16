# 2026-09-12 invocation readiness gate

## 修正

主进程的业务 readiness 现在要求 `RuntimeExecutor` 显式配置
`InvocationProbe`。Kubernetes Deployment/LWS ready、持久化 model ready 和
调用健康是三个独立事实；没有调用探针时保持未就绪，不能把运行时对象已
创建误报为推理服务已经可调用。

## 证据

- `TestRuntimeExecutorReadinessRequiresExplicitInvocationProbe` 验证探针缺失
  和已配置两种状态。
- Kubernetes 和 cmd 定向测试通过。
- 当前没有真实数据面 endpoint、探针协议或引擎依赖，因此这些仍为
  `not_verified`；不会用 readiness gate 代替真实调用验证。
