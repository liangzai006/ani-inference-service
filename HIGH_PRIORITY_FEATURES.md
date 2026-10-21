# 高优先级功能快速参考

## 实施状态：✅ 完成

所有三个高优先级功能已实现并通过测试。

---

## 1. 健康检查 (Health Check)

### 文件位置
- 实现: [`internal/service/health.go`](internal/service/health.go)
- 测试: [`internal/service/health_test.go`](internal/service/health_test.go)

### gRPC 方法
```protobuf
rpc GetServiceHealth(GetServiceHealthRequest) returns (GetServiceHealthResponse);
```

### 使用方式

```go
// gRPC 调用
resp, err := client.GetServiceHealth(ctx, &inferencev1.GetServiceHealthRequest{
    TenantId:  "tenant-123",
    ServiceId: "service-abc",
})

// 检查整体状态
switch resp.OverallStatus {
case "healthy":
    // 服务正常
case "degraded":
    // 服务部分功能受限
case "unhealthy":
    // 服务故障
case "unknown":
    // 无法确定状态
}

// 查看详细检查结果
for _, check := range resp.Checks {
    fmt.Printf("%s: %s - %s\n", 
        check.Dimension, 
        check.Status, 
        check.Message)
}
```

### 检查维度

| 维度 | 说明 | 健康标准 |
|------|------|----------|
| runtime_phase | Kubernetes Pod 运行状态 | ready |
| publication_phase | 服务发布状态 | published |
| invocation_health | 调用健康状态 | healthy |
| observation_staleness | 观察数据新鲜度 | < 5分钟 |

### 状态等级
- **healthy**: 所有检查通过
- **degraded**: 部分功能受限（如观察数据过时）
- **unhealthy**: 服务故障（如 Pod 失败）
- **unknown**: 无法确定状态

---

## 2. 指标收集 (Metrics Collection)

### 文件位置
- 实现: [`internal/service/metrics.go`](internal/service/metrics.go)
- 测试: [`internal/service/metrics_test.go`](internal/service/metrics_test.go)

### gRPC 方法
```protobuf
rpc GetServiceMetrics(GetServiceMetricsRequest) returns (GetServiceMetricsResponse);
```

### 使用方式

```go
// gRPC 调用
resp, err := client.GetServiceMetrics(ctx, &inferencev1.GetServiceMetricsRequest{
    TenantId:  "tenant-123",
    ServiceId: "service-abc",
})

// 请求指标
fmt.Printf("总请求数: %d\n", resp.TotalRequests)
fmt.Printf("成功率: %.2f%%\n", resp.SuccessRate)

// 延迟指标
fmt.Printf("平均延迟: %.2f ms\n", resp.LatencyStats.MeanMs)
fmt.Printf("P95 延迟: %.2f ms\n", resp.LatencyStats.P95Ms)
fmt.Printf("P99 延迟: %.2f ms\n", resp.LatencyStats.P99Ms)

// CPU 使用
cpu := resp.ResourceUsage.CpuUsage
fmt.Printf("CPU 平均: %.2f%%, 峰值: %.2f%%\n", 
    cpu.AverageUtilizationPercent,
    cpu.PeakUtilizationPercent)

// 内存使用
mem := resp.ResourceUsage.MemoryUsage
fmt.Printf("内存 平均: %d bytes, 峰值: %d bytes\n",
    mem.AverageBytes,
    mem.PeakBytes)

// GPU 使用（如果有）
if gpu := resp.ResourceUsage.Gpu; gpu != nil {
    fmt.Printf("GPU 利用率 平均: %.2f%%, 峰值: %.2f%%\n",
        gpu.AverageUtilizationPercent,
        gpu.PeakUtilizationPercent)
    fmt.Printf("GPU 内存 平均: %d bytes, 峰值: %d bytes\n",
        gpu.AverageMemoryBytes,
        gpu.PeakMemoryBytes)
}
```

### 指标类型

#### 请求指标
- `TotalRequests`: 总请求数
- `SuccessfulRequests`: 成功请求数
- `FailedRequests`: 失败请求数
- `SuccessRate`: 成功率（百分比）

#### 延迟指标
- `MeanMs`: 平均延迟
- `P50Ms`: 中位数延迟
- `P95Ms`: 95% 请求的延迟
- `P99Ms`: 99% 请求的延迟

#### 资源指标
- **CPU**: 平均/峰值利用率（%）
- **内存**: 平均/峰值使用量（bytes）
- **GPU**: 平均/峰值利用率（%）和显存（bytes）

### Prometheus 集成

项目包含 Prometheus 查询构建器，可生成标准的 PromQL 查询：

```go
queries := PrometheusQueryBuilder{}.BuildQueries(
    "tenant-123",
    "service-abc",
    "inference-system",
)

// queries["request_rate"]: 请求速率查询
// queries["success_rate"]: 成功率查询
// queries["latency_p95"]: P95 延迟查询
// queries["latency_p99"]: P99 延迟查询

resourceQueries := PrometheusQueryBuilder{}.BuildResourceQueries(
    "tenant-123",
    "service-abc",
    "inference-system",
)

// resourceQueries["cpu_usage"]: CPU 使用查询
// resourceQueries["memory_usage"]: 内存使用查询
// resourceQueries["gpu_utilization"]: GPU 利用率查询
// resourceQueries["gpu_memory"]: GPU 内存查询
```

**注意:** 当前实现返回模拟数据。要启用真实的 Prometheus 集成，需要：
1. 添加 `prometheus/client_golang` 依赖
2. 配置 Prometheus 连接
3. 在 `MetricsCollector` 中执行查询

---

## 3. 测试调用 (Test Invocation)

### 文件位置
- 实现: [`internal/service/invocation.go`](internal/service/invocation.go)
- 测试: [`internal/service/invocation_test.go`](internal/service/invocation_test.go)

### gRPC 方法
```protobuf
rpc TestInvocation(TestInvocationRequest) returns (TestInvocationResponse);
```

### 使用方式

```go
// 基本调用
resp, err := client.TestInvocation(ctx, &inferencev1.TestInvocationRequest{
    TenantId:    "tenant-123",
    ServiceId:   "service-abc",
    EndpointUrl: "https://inference.example.com/v1/chat/completions",
    RequestBody: `{
        "model": "gpt-4",
        "messages": [{"role": "user", "content": "Hello"}]
    }`,
})

// 检查结果
if resp.StatusCode == 200 {
    fmt.Printf("✅ 调用成功 (延迟: %d ms)\n", resp.LatencyMs)
    fmt.Printf("响应: %s\n", resp.ResponseBody)
} else {
    fmt.Printf("❌ 调用失败 (状态码: %d)\n", resp.StatusCode)
    fmt.Printf("错误: %s\n", resp.ErrorMessage)
}

// 带认证的调用
respAuth, err := client.TestInvocation(ctx, &inferencev1.TestInvocationRequest{
    TenantId:            "tenant-123",
    ServiceId:           "service-abc",
    EndpointUrl:         "https://inference.example.com/v1/chat/completions",
    RequestBody:         `{"model":"gpt-4","messages":[...]}`,
    AuthorizationHeader: "Bearer sk-abc123...",
})
```

### 请求参数

| 参数 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `tenant_id` | string | ✅ | 租户 ID |
| `service_id` | string | ✅ | 服务 ID |
| `endpoint_url` | string | ✅ | 推理服务端点 URL |
| `request_body` | string | ✅ | JSON 格式的请求体 |
| `authorization_header` | string | ❌ | 可选的认证 header |

### 响应字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `status_code` | int32 | HTTP 状态码 (200, 401, 500, etc.) |
| `response_body` | string | 响应体内容 |
| `latency_ms` | int64 | 请求延迟（毫秒） |
| `error_message` | string | 错误信息（如果有） |

### OpenAI 格式验证

```go
// 验证 OpenAI 兼容格式
err := ValidateOpenAIFormat(requestBody)
if err != nil {
    // 不符合 OpenAI 格式
}

// OpenAI 格式要求:
// 1. 必须包含 "model" 字段
// 2. 必须包含 "messages" 或 "prompt" 字段之一
```

### 构建默认请求

```go
// 生成默认的 OpenAI 兼容请求
defaultRequest := BuildDefaultOpenAIRequest("gpt-4")
// 返回: {"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}
```

### 超时和限制
- **请求超时**: 30 秒
- **最大响应大小**: 无限制（由 HTTP 客户端决定）
- **并发限制**: 无（由调用方控制）

---

## 测试

### 运行所有测试

```bash
cd /root/kubercon/ani-inference-service
go test ./internal/service/... -v
```

### 运行特定功能测试

```bash
# 健康检查测试
go test ./internal/service/... -v -run TestHealthChecker

# 指标收集测试
go test ./internal/service/... -v -run TestMetrics

# 调用测试
go test ./internal/service/... -v -run TestInvocation
```

### 测试覆盖率

```bash
go test ./internal/service/... -cover
```

当前测试覆盖率：
- ✅ 健康检查: 完整覆盖（5 个测试组）
- ✅ 指标收集: 完整覆盖（4 个测试组）
- ✅ 测试调用: 完整覆盖（3 个测试组）

---

## 集成到现有服务

所有三个功能已集成到 `InferenceServer` 中，通过以下方式初始化：

```go
// 在 cmd/ani-inference-service/wire.go 或启动代码中
inferenceServer := &service.InferenceServer{
    usecase:          inferenceUseCase,
    log:              logger,
    healthChecker:    service.NewHealthChecker(),
    metricsCollector: service.NewMetricsCollector(),
    invocationTester: service.NewInvocationTester(),
}
```

gRPC 服务器会自动处理这些方法的路由。

---

## 依赖项

### 已有依赖（无需额外安装）
- ✅ `k8s.io/client-go`: Kubernetes 客户端
- ✅ `google.golang.org/grpc`: gRPC
- ✅ `google.golang.org/protobuf`: Protobuf

### 标准库（Go 内置）
- `net/http`: HTTP 客户端
- `encoding/json`: JSON 处理
- `time`: 时间测量
- `context`: 上下文管理

### 可选依赖（Prometheus 实时集成）
如需启用真实的 Prometheus 指标查询（而非模拟数据），添加：

```bash
go get github.com/prometheus/client_golang/api
go get github.com/prometheus/client_golang/api/prometheus/v1
```

---

## 监控和告警建议

### 健康检查
建议监控项：
- `OverallStatus != "healthy"` → 触发告警
- `observation_staleness = "unhealthy"` → 数据管道问题
- `runtime_phase = "unhealthy"` → Pod 故障

### 指标
建议告警阈值：
- 成功率 < 95% → 警告
- 成功率 < 90% → 严重
- P99 延迟 > 5 秒 → 警告
- CPU 利用率 > 90% → 警告
- GPU 利用率 < 10%（GPU 服务）→ 资源浪费

### 测试调用
建议使用场景：
- 部署前验证
- 定期健康探测（每 5-10 分钟）
- 用户报告问题时的诊断工具

---

## 故障排查

### 健康检查返回 "unknown"
- 检查服务是否存在
- 检查 Kubernetes API 连接
- 验证租户和服务 ID 是否正确

### 指标数据为 0
- 当前版本返回模拟数据
- 要获取真实数据，需集成 Prometheus 客户端
- 确认 Prometheus 中有相应的指标数据

### 测试调用失败
- 检查 `endpoint_url` 是否正确
- 验证网络连接（防火墙、DNS）
- 检查认证 token 是否有效
- 查看 `error_message` 字段获取详细错误

---

## 下一步计划

### 短期（1-2 周）
- [ ] 集成真实的 Prometheus 客户端
- [ ] 添加健康检查缓存（减少 Kubernetes API 调用）
- [ ] 支持批量测试调用

### 中期（1-2 个月）
- [ ] 添加更多指标维度（网络、磁盘 I/O）
- [ ] 支持自定义健康检查规则
- [ ] 测试调用支持更多 HTTP 方法（GET, PUT, DELETE）

### 长期（3-6 个月）
- [ ] 机器学习驱动的异常检测
- [ ] 自动化性能调优建议
- [ ] 多区域健康状态聚合

---

## 相关文档

- [完整实施总结](IMPLEMENTATION_SUMMARY.md)
- [API 文档](api/inference/v1/README.md)
- [开发指南](docs/DEVELOPMENT.md)

---

## 联系方式

如有问题或需要支持，请联系：
- 开发团队: [team@example.com](mailto:team@example.com)
- 问题跟踪: [GitHub Issues](https://github.com/liangzai006/ani-inference-service/issues)
