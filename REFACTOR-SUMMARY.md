# Model API 重构总结

## 日期
2026-10-20

## 背景
原本 `ani-inference-service/api/model/v1/` 中包含了 Model Service 的 protobuf 定义，这导致了混乱的依赖关系和 API 所有权不清晰。

## 重构内容

### 1. 删除了冗余的 Model API
从 `ani-inference-service/api/` 中删除了：
- `api/model/v1/model.proto`
- `api/model/v1/model.pb.go`
- `api/model/v1/model_grpc.pb.go`

**总共删除**: 2,784 行代码

### 2. 更新了所有导入路径
将所有对 Model API 的引用从：
```go
import modelv1 "github.com/liangzai006/ani-inference-service/api/model/v1"
```

改为：
```go
import modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
```

**涉及文件**:
- `internal/biz/inference/business_payload.go`
- `internal/biz/inference/business_validation.go`
- `internal/biz/inference/validate.go`
- `internal/biz/inference/business_payload_test.go`
- 以及其他多个测试和业务文件

### 3. 更新了 go.mod 依赖
在 `ani-inference-service/api/go.mod` 中添加了对 model-service API 的 replace：
```go
replace github.com/liangzai006/ani-model-service/api => ../../ani-model-service/api
```

在 `ani-model-service/go.mod` 中添加了对自己 API 子模块的依赖：
```go
require (
    github.com/liangzai006/ani-model-service/api v0.1.0
    ...
)

replace github.com/liangzai006/ani-model-service/api => ./api
```

## 验证结果

### ani-inference-service
```bash
$ go build ./...
# 成功

$ go test ./...
# 所有测试通过
```

### ani-model-service
```bash
$ go build ./...
# 成功

$ make test
# 所有测试通过
```

## 影响范围

### ✅ 无破坏性变更
- 所有 gRPC 服务定义未改变
- 所有消息类型未改变
- 只是改变了代码的物理位置和导入路径

### ✅ 清晰的依赖关系
- inference-service **依赖** model-service 的 API
- model-service 拥有并维护自己的 API

### ✅ 维护性提升
- API 定义现在在正确的仓库中
- 避免了循环依赖的可能性
- 更清晰的服务边界

## 后续工作

无需额外工作。所有代码已经编译通过，所有测试已经验证。

## 提交记录

**ani-inference-service**:
- `refactor: remove model API and update imports to model-service`
  - 删除冗余的 model proto 文件
  - 更新所有导入路径到 model-service

**ani-model-service**:
- `build: add api submodule to go.mod`
  - 添加对 api 子模块的正式依赖

---
重构完成 ✓
