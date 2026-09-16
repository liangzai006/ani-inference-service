# 2026-09-14 PG + Kubernetes 正式进程预检

在 PostgreSQL 测试容器内启动当前静态 Inference binary，设置：

- `ANI_DATABASE_DSN`：容器 Unix socket 的 `ani_inference` 数据库；
- `ANI_KUBERNETES_ENABLED=true`；
- `ANI_KUBECONFIG`：当前 `kubernetes-admin@kubernetes` 的临时 flattened kubeconfig；
- `ANI_INFERENCE_NAMESPACE=ani-inference-e2e-20260914`。

进程成功建立 PG/Kubernetes 组合并打开 Kratos listeners。探针结果：

- `/healthz`：HTTP 200；
- `/readyz`：HTTP 503。

503 是预期结果：当前正式组合尚未配置真实 admission、model、quota、publication 和
invocation provider，`ReadyCheck` 不允许把持久 operation 交给不完整 worker。临时进程
已停止，未创建 Inference runtime workload。该结果证明组合启动和 readiness 门禁，
不证明业务 operation 已在真实 provider 上执行。
