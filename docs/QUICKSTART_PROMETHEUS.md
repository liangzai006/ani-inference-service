# Prometheus 实时指标启用快速指南

## 🎯 目标

将 `GetServiceMetrics` 从返回模拟数据切换到从 Prometheus 查询实时指标。

## ✅ 前置条件

1. **Prometheus 已部署**
   ```bash
   kubectl get svc -n monitoring prometheus-k8s
   # 应返回: prometheus-k8s   ClusterIP   10.x.x.x   <none>   9090/TCP
   ```

2. **推理服务 Pod 已暴露指标**
   ```bash
   # 检查 Pod 的 /metrics 端点
   kubectl exec -n inference-system <pod-name> -- curl localhost:9090/metrics | grep inference_requests_total
   ```

3. **ServiceMonitor 已配置** (如果使用 Prometheus Operator)
   ```yaml
   apiVersion: monitoring.coreos.com/v1
   kind: ServiceMonitor
   metadata:
     name: inference-services
     namespace: inference-system
   spec:
     selector:
       matchLabels:
         app: inference-service
     endpoints:
       - port: metrics
         interval: 15s
   ```

## 📝 实施步骤

### 步骤 1: 扩展配置结构

编辑 `cmd/ani-inference-service/config.go` (如果该文件不存在，创建它):

```go
package main

import "os"

type Config struct {
    // 现有配置...
    
    // Prometheus 集成
    PrometheusURL string
    Namespace     string
}

func LoadConfig() *Config {
    return &Config{
        // 现有配置加载...
        
        PrometheusURL: getEnv("PROMETHEUS_URL", ""),
        Namespace:     getEnv("INFERENCE_NAMESPACE", "inference-system"),
    }
}

func getEnv(key, defaultValue string) string {
    if value := os.Getenv(key); value != "" {
        return value
    }
    return defaultValue
}
```

### 步骤 2: 修改 InferenceServer 结构

编辑 `internal/service/inference.go`:

```go
type InferenceServer struct {
    inferencev1.UnimplementedInferenceServiceServer
    
    // 现有字段...
    
    // 新增: Metrics 收集器
    metricsCollector *MetricsCollector
}
```

### 步骤 3: 在构造函数中初始化 MetricsCollector

编辑 `cmd/ani-inference-service/main.go` (或 `inference.go` 中的构造函数):

```go
func NewInferenceServer(
    cfg *Config,
    inferenceUC *biz.InferenceUseCase,
    // ... 其他依赖
) *service.InferenceServer {
    
    // 初始化 MetricsCollector
    metricsCollector := service.NewMetricsCollector(
        cfg.PrometheusURL,
        cfg.Namespace,
    )
    
    return &service.InferenceServer{
        // 现有字段初始化...
        metricsCollector: metricsCollector,
    }
}
```

### 步骤 4: 使用预配置的 Collector

编辑 `internal/service/inference.go` 中的 `GetServiceMetrics` 方法:

```go
func (s *InferenceServer) GetServiceMetrics(
    ctx context.Context,
    in *inferencev1.GetServiceMetricsRequest,
) (*inferencev1.GetServiceMetricsResponse, error) {
    
    tenant, err := RequireTenant(ctx, s.WorkloadID)
    if err != nil {
        return nil, err
    }
    
    svc, err := s.inferenceUC.GetService(ctx, tenant, in.ResourceId)
    if err != nil {
        return nil, err
    }
    
    // 使用预配置的 collector (替换原来每次创建新 collector 的代码)
    return s.metricsCollector.CollectServiceMetrics(ctx, tenant, svc)
}
```

### 步骤 5: 配置环境变量

编辑 Kubernetes Deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ani-inference-service
  namespace: inference-system
spec:
  template:
    spec:
      containers:
      - name: inference-service
        image: your-registry/ani-inference-service:latest
        env:
          # 新增: Prometheus 配置
          - name: PROMETHEUS_URL
            value: "http://prometheus-k8s.monitoring.svc.cluster.local:9090"
          
          - name: INFERENCE_NAMESPACE
            valueFrom:
              fieldRef:
                fieldPath: metadata.namespace
          
          # 现有环境变量...
```

### 步骤 6: 重新部署

```bash
# 构建新镜像
make docker-build

# 推送镜像
make docker-push

# 应用新配置
kubectl apply -f deploy/inference-service.yaml

# 滚动更新
kubectl rollout restart deployment/ani-inference-service -n inference-system

# 检查 Pod 状态
kubectl get pods -n inference-system -l app=inference-service
```

### 步骤 7: 验证

```bash
# 1. 检查日志 (可选: 添加调试日志)
kubectl logs -n inference-system <pod-name> | grep Prometheus

# 2. 调用 API 测试
grpcurl -plaintext \
  -H "x-ani-tenant-id: <租户UUID>" \
  -d '{"resource_id": "<服务UUID>"}' \
  localhost:50051 \
  inference.v1.InferenceService/GetServiceMetrics

# 3. 检查返回数据
# 如果配置成功，应该看到真实的请求数、延迟等数据
# 如果 Prometheus 不可用，会自动降级到模拟数据 (不报错)
```

## 🔍 故障排查

### 问题 1: 返回的指标都是模拟值

**症状:** `total_requests` 总是 15420 或 8520

**检查清单:**

```bash
# 1. 验证环境变量已注入
kubectl exec -n inference-system <pod-name> -- env | grep PROMETHEUS
# 应该看到: PROMETHEUS_URL=http://...

# 2. 测试 Prometheus 连通性
kubectl exec -n inference-system <pod-name> -- \
  curl -s "$PROMETHEUS_URL/api/v1/query?query=up" | jq .

# 3. 检查推理服务的指标是否被采集
kubectl exec -n inference-system <pod-name> -- \
  curl -s "$PROMETHEUS_URL/api/v1/query?query=inference_requests_total"
```

### 问题 2: Prometheus 查询返回空结果

**症状:** 所有指标字段都是 0

**可能原因:**

1. **标签不匹配**
   ```bash
   # 检查实际的标签
   curl "http://prometheus:9090/api/v1/series?match[]=inference_requests_total"
   ```

2. **时间范围太短**
   ```bash
   # 检查指标的最早时间戳
   curl "http://prometheus:9090/api/v1/query?query=inference_requests_total{service_id=\"xxx\"}"
   ```

3. **指标名称错误**
   ```bash
   # 列出所有可用指标
   curl "http://prometheus:9090/api/v1/label/__name__/values" | jq .
   ```

### 问题 3: GPU 指标缺失

**检查:**

```bash
# 1. 确认 DCGM Exporter 运行中
kubectl get pods -n gpu-operator -l app=nvidia-dcgm-exporter

# 2. 检查 GPU 指标是否存在
kubectl exec -n gpu-operator <dcgm-pod> -- \
  curl localhost:9400/metrics | grep nvidia_gpu_duty_cycle

# 3. 验证 ServiceMonitor 配置
kubectl get servicemonitor -n gpu-operator nvidia-dcgm-exporter -o yaml
```

## 🧪 本地开发测试

如果没有 Prometheus 但想测试代码逻辑:

```go
// 在测试中使用模拟的 Prometheus 响应
func TestWithMockPrometheus(t *testing.T) {
    // 启动 httptest 服务器
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 返回模拟的 Prometheus API 响应
        json.NewEncoder(w).Encode(map[string]interface{}{
            "status": "success",
            "data": map[string]interface{}{
                "resultType": "vector",
                "result": []map[string]interface{}{
                    {
                        "value": []interface{}{time.Now().Unix(), "1234.56"},
                    },
                },
            },
        })
    }))
    defer server.Close()
    
    // 使用测试服务器地址
    collector := NewMetricsCollector(server.URL, "test-namespace")
    // ...
}
```

## 📊 预期效果对比

### 启用前 (模拟数据)

```json
{
  "total_requests": 15420,
  "successful_requests": 15389,
  "failed_requests": 31,
  "success_rate": 99.8,
  "latency": {
    "p50_ms": 45,
    "p95_ms": 120,
    "p99_ms": 280,
    "max_ms": 890
  }
}
```

### 启用后 (Prometheus 实时数据)

```json
{
  "total_requests": 237891,       // ← 真实请求计数
  "successful_requests": 237103,
  "failed_requests": 788,
  "success_rate": 99.67,          // ← 真实成功率
  "latency": {
    "p50_ms": 38.2,              // ← 真实延迟统计
    "p95_ms": 142.8,
    "p99_ms": 324.5,
    "max_ms": 1234.7
  },
  "resource_usage": {
    "cpu": {
      "average_cores": 2.3,       // ← 真实 CPU 使用
      "peak_cores": 3.8
    },
    "memory": {
      "average_bytes": 4294967296, // 4 GiB
      "peak_bytes": 5368709120     // 5 GiB
    }
  }
}
```

## 🔄 回滚方案

如果启用后出现问题，可以快速回滚:

```bash
# 方法 1: 移除环境变量 (自动降级到模拟数据)
kubectl set env deployment/ani-inference-service \
  -n inference-system \
  PROMETHEUS_URL-

# 方法 2: 回滚到上一个版本
kubectl rollout undo deployment/ani-inference-service -n inference-system
```

系统会自动降级到模拟数据，**不会中断服务**。

## 📚 相关文档

- [完整技术文档](./prometheus-metrics-integration.md)
- [Prometheus 查询语法](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [推理服务指标规范](./metrics-specification.md) (如果存在)

## ✨ 完成检查

- [ ] Config 结构添加 PrometheusURL 和 Namespace 字段
- [ ] InferenceServer 添加 metricsCollector 字段
- [ ] 构造函数初始化 MetricsCollector
- [ ] GetServiceMetrics 使用预配置的 collector
- [ ] Deployment 配置环境变量
- [ ] 重新构建和部署
- [ ] 验证返回真实数据
- [ ] (可选) 添加监控和告警

---

**预计实施时间:** 30 分钟  
**影响范围:** 仅 `GetServiceMetrics` API  
**向后兼容:** ✅ 是 (自动降级保证兼容)
