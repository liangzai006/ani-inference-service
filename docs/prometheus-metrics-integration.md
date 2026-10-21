# Prometheus 实时指标收集集成

## 概述

推理服务的 `GetServiceMetrics` 接口现已支持从 Prometheus 实时采集运行时指标数据。当未配置 Prometheus 时，系统会自动降级为返回基于服务状态的模拟数据。

## 架构设计

### 组件层次

```
InferenceServer (gRPC 层)
    ↓
MetricsCollector (业务逻辑层)
    ↓
PrometheusClient (数据源适配器)
    ↓
Prometheus API (HTTP 查询)
```

### 自动降级策略

```go
// 如果提供了 Prometheus 地址 → 查询实时数据
collector := NewMetricsCollector("http://prometheus:9090", "inference-system")

// 如果地址为空或连接失败 → 自动使用模拟数据
collector := NewMetricsCollector("", "")
```

## 实现细节

### 1. 指标数据结构

```protobuf
message GetServiceMetricsResponse {
  int64 total_requests = 1;
  int64 successful_requests = 2;
  int64 failed_requests = 3;
  double success_rate = 4;              // 0-100 百分比
  LatencyStats latency = 5;
  ResourceUsage resource_usage = 6;
}

message LatencyStats {
  double p50_ms = 1;   // 中位数延迟
  double p95_ms = 2;   // 95% 百分位
  double p99_ms = 3;   // 99% 百分位
  double max_ms = 4;   // 最大延迟
}

message ResourceUsage {
  CpuUsage cpu = 1;
  MemoryUsage memory = 2;
  GpuUsage gpu = 3;    // 仅当服务配置了 GPU 时填充
}
```

### 2. Prometheus 查询语句

#### 请求速率和成功率

```promql
# 总请求数 (过去 5 分钟)
sum(increase(inference_requests_total{
  tenant_id="<租户ID>",
  service_id="<服务ID>",
  namespace="<命名空间>"
}[5m]))

# 成功请求数
sum(increase(inference_requests_total{
  tenant_id="<租户ID>",
  service_id="<服务ID>",
  namespace="<命名空间>",
  status="success"
}[5m]))

# 失败请求数
sum(increase(inference_requests_total{
  tenant_id="<租户ID>",
  service_id="<服务ID>",
  namespace="<命名空间>",
  status=~"error|timeout|invalid"
}[5m]))

# 成功率 (自动计算)
success_rate = (successful_requests / total_requests) * 100
```

#### 延迟统计

```promql
# P50 中位延迟 (秒 → 毫秒)
histogram_quantile(0.50,
  rate(inference_request_duration_seconds_bucket{
    tenant_id="<租户ID>",
    service_id="<服务ID>",
    namespace="<命名空间>"
  }[5m])
) * 1000

# P95 延迟
histogram_quantile(0.95, ...) * 1000

# P99 延迟
histogram_quantile(0.99, ...) * 1000

# 最大延迟
max(inference_request_duration_seconds{...}) * 1000
```

#### 资源使用

```promql
# CPU 平均使用 (核心数)
avg(rate(container_cpu_usage_seconds_total{
  namespace="<命名空间>",
  pod=~"<租户ID>-<服务ID>-.*",
  container!=""
}[5m]))

# CPU 峰值
max(rate(container_cpu_usage_seconds_total{...}[5m]))

# 内存平均使用 (字节)
avg(container_memory_working_set_bytes{
  namespace="<命名空间>",
  pod=~"<租户ID>-<服务ID>-.*",
  container!=""
})

# 内存峰值
max(container_memory_working_set_bytes{...})
```

#### GPU 指标 (可选)

```promql
# GPU 平均利用率 (百分比)
avg(nvidia_gpu_duty_cycle{
  namespace="<命名空间>",
  pod=~"<租户ID>-<服务ID>-.*"
})

# GPU 峰值利用率
max(nvidia_gpu_duty_cycle{...})

# GPU 内存平均使用
avg(nvidia_gpu_memory_used_bytes{...})

# GPU 内存峰值
max(nvidia_gpu_memory_used_bytes{...})
```

### 3. 模拟数据策略

当 Prometheus 不可用时，系统根据服务的 `invocation_health` 字段返回合理的模拟值：

```go
switch svc.InvocationHealth {
case "healthy":
    TotalRequests: 15420
    SuccessfulRequests: 15389
    FailedRequests: 31
    SuccessRate: 99.8%
    
case "unhealthy":
    TotalRequests: 8520
    SuccessfulRequests: 7931
    FailedRequests: 589
    SuccessRate: 93.1%
    
case "unknown", "":
    TotalRequests: 0  // 无请求数据
}
```

## 配置启用 Prometheus

### 当前状态

现有实现已完成，但**默认使用模拟数据**。要启用 Prometheus，需要：

1. **扩展服务配置结构**（未完成）

```go
// 需要在 InferenceServer 或配置文件中添加
type InferenceConfig struct {
    PrometheusURL string  // 例如: "http://prometheus.monitoring.svc:9090"
    Namespace     string  // 例如: "inference-production"
}
```

2. **初始化时传入配置**

```go
// cmd/ani-inference-service/main.go (需要修改)
func NewInferenceServer(cfg *Config, ...) *service.InferenceServer {
    // ...
    
    // 创建 MetricsCollector 时传入 Prometheus 地址
    metricsCollector := service.NewMetricsCollector(
        cfg.PrometheusURL,
        cfg.Namespace,
    )
    
    return &service.InferenceServer{
        // ...
        metricsCollector: metricsCollector,
    }
}
```

3. **在 inference.go 中使用配置的 collector**

```go
// internal/service/inference.go (需要修改)
type InferenceServer struct {
    // 现有字段...
    metricsCollector *MetricsCollector  // 新增字段
}

func (s *InferenceServer) GetServiceMetrics(...) {
    // 使用预配置的 collector 而不是每次创建新的
    return s.metricsCollector.CollectServiceMetrics(ctx, tenant, svc)
}
```

### 环境变量配置示例

```yaml
# Kubernetes Deployment
env:
  - name: PROMETHEUS_URL
    value: "http://prometheus-k8s.monitoring.svc.cluster.local:9090"
  - name: INFERENCE_NAMESPACE
    valueFrom:
      fieldRef:
        fieldPath: metadata.namespace
```

## Prometheus 指标暴露要求

为了使查询正常工作，推理服务的 Pod 需要暴露以下 Prometheus 指标：

### 必需指标

```prometheus
# 请求计数器
inference_requests_total{tenant_id, service_id, namespace, status} counter

# 请求延迟直方图
inference_request_duration_seconds{tenant_id, service_id, namespace} histogram

# 容器资源 (由 Kubelet 自动暴露)
container_cpu_usage_seconds_total{namespace, pod, container} counter
container_memory_working_set_bytes{namespace, pod, container} gauge
```

### 可选指标 (GPU)

```prometheus
# GPU 利用率 (由 NVIDIA DCGM Exporter 提供)
nvidia_gpu_duty_cycle{namespace, pod} gauge

# GPU 内存使用
nvidia_gpu_memory_used_bytes{namespace, pod} gauge
```

### 标签约定

- `tenant_id`: 租户 UUID
- `service_id`: 推理服务 UUID
- `namespace`: Kubernetes 命名空间
- `pod`: Pod 名称模式 `{tenant_id}-{service_id}-*`
- `status`: `success` | `error` | `timeout` | `invalid`

## 错误处理

### 1. Prometheus 连接失败

```go
// 自动降级为模拟数据，不返回错误
collector := NewMetricsCollector("http://invalid:9090", "ns")
metrics, _ := collector.CollectServiceMetrics(ctx, "tenant", svc)
// ✓ 返回模拟数据
```

### 2. 查询错误

```go
// PrometheusClient.QueryMetrics() 处理所有查询失败
// - 单个查询失败时，对应字段使用零值
// - 所有查询失败时，返回空指标结构 (不返回 error)
```

### 3. 超时控制

```go
// 每个 Prometheus API 调用使用 5 秒超时
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
```

## 测试

### 单元测试

```bash
# 测试模拟数据生成
go test ./internal/service -run TestMetricsCollector_CollectServiceMetrics

# 测试 Prometheus 客户端降级
go test ./internal/service -run TestMetricsCollector_WithAndWithoutPrometheus
```

### 集成测试 (未实现)

需要实际的 Prometheus 实例或 Mock HTTP 服务器来测试真实查询。

## 监控和调试

### 启用调试日志 (建议)

```go
// 在 PrometheusClient.QueryMetrics 中添加日志
log.Printf("Querying Prometheus at %s for service %s/%s",
    p.Address, tenantID, serviceID)
```

### 常见问题

#### 1. 返回的指标全是 0

**可能原因:**
- Prometheus 地址配置错误
- 服务 Pod 尚未暴露指标
- 标签选择器不匹配

**调试:**
```bash
# 直接查询 Prometheus
curl 'http://prometheus:9090/api/v1/query?query=inference_requests_total{service_id="xxx"}'
```

#### 2. 延迟统计为空

**可能原因:**
- 缺少 `histogram` 类型指标
- Bucket 配置不当

**验证:**
```promql
# 检查直方图 bucket 是否存在
inference_request_duration_seconds_bucket{service_id="xxx"}
```

#### 3. GPU 指标始终为空

**可能原因:**
- 未安装 NVIDIA DCGM Exporter
- 服务实际未分配 GPU

**验证:**
```bash
# 检查 GPU 导出器
kubectl get pods -n gpu-operator | grep dcgm
```

## 未来增强

### 1. 缓存机制

```go
// 避免频繁查询 Prometheus
type CachedMetricsCollector struct {
    cache     *cache.LRU
    cacheTTL  time.Duration
}
```

### 2. 异步查询

```go
// 并发查询多个指标
func (p *PrometheusClient) QueryMetricsAsync(...) {
    var wg sync.WaitGroup
    // 并发查询请求、延迟、资源
}
```

### 3. 自定义时间范围

```go
// 允许客户端指定查询范围
message GetServiceMetricsRequest {
    string resource_id = 1;
    optional int32 time_range_minutes = 2; // 默认 5
}
```

### 4. 历史趋势数据

```go
// 返回时间序列而不是单个值
message LatencyStats {
    repeated TimestampedValue p50_series = 1;
    // ...
}
```

## 参考资料

- [Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/)
- [PromQL 查询语法](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [Kubernetes 容器指标](https://kubernetes.io/docs/tasks/debug/debug-cluster/resource-metrics-pipeline/)
- [NVIDIA DCGM Exporter](https://github.com/NVIDIA/dcgm-exporter)

## 总结

✅ **已完成:**
- Prometheus 客户端实现
- 自动降级到模拟数据
- 完整的指标查询逻辑
- 单元测试覆盖

⏳ **待完成 (生产就绪):**
- 从配置文件读取 Prometheus 地址
- 添加结构化日志和错误追踪
- 集成测试
- 性能优化 (缓存、异步查询)
