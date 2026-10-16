# Inference 单 gRPC 入口与 GPU 合同设计

## 目标

Inference 只使用普通 gRPC 入口 `19090`。查询、CPU 生命周期和 Governance 发起的 GPU 合同命令共享同一套 TLS 1.3、服务身份、租户和操作人上下文。

## 认证与租户绑定

普通 gRPC 中间件先验证客户端 DNS SAN、`x-ani-tenant-id` 和 `x-ani-actor`，再把租户与操作人写入上下文。GPU 创建或删除请求还必须携带 `gpu_owner_attachment`；附件租户必须与已验证的 `x-ani-tenant-id` 完全一致，随后才在上下文标记 Governance 合同身份。附件字段不能单独建立租户上下文。

查询、CPU 创建和 CPU 删除不会设置 GPU 合同身份。Inference 的 GPU 合同校验继续要求该标记、租户一致性、冻结计划、原始配额和请求摘要。

## 入口与配置

移除独立 managed receiver 的监听器和配置。`19092` 不再由应用或 Helm Chart 创建；`managed_gpu` 只保留向 Governance 上报退款所需的客户端配置。Governance 继续使用单一 `ANI_INFERENCE_ADDR` 指向 `19090`。

## 验证

- 普通查询带租户元数据可以通过，缺失或冲突元数据被拒绝。
- 带匹配 GPU 附件的 Governance 请求在 `19090` 获得 GPU 合同身份。
- GPU 附件租户与认证租户不一致时被拒绝。
- Inference 进程只注册一个业务 gRPC 监听器；Helm Service 只暴露 `19090` 和 admin `19091`。
