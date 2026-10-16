# Inference 单 gRPC GPU 入口实施计划

> **For agentic workers:** Follow the checked steps in order and run each test before moving to the next step.

**Goal:** 将普通查询、CPU 生命周期和 GPU 合同命令统一到 Inference 的 `19090` gRPC 入口。

**Architecture:** 普通 TLS 中间件负责认证租户和操作人；对带 GPU owner attachment 的 Governance 创建/删除请求追加合同身份标记并校验附件租户。删除独立 managed receiver，避免同一 RPC 在第二个端口缺少查询租户上下文。

**Tech Stack:** Go、Kratos gRPC、protobuf、Helm、Kubernetes。

## Global Constraints

- 只使用 TLS 1.3 和已验证 DNS SAN；不增加明文或跳过验证路径。
- Governance 继续通过单一 `ANI_INFERENCE_ADDR` 调用 Inference。
- GPU 附件只做合同一致性校验，不能单独建立租户上下文。
- 通过仓库现有生成脚本生成 protobuf；不手改生成文件。

### Task 1: 普通入口的 GPU 身份绑定

**Files:**
- Modify: `cmd/ani-inference-service/trusted_tenant.go`
- Test: `cmd/ani-inference-service/rpc_policy_test.go`

- [x] 已先写入失败测试，覆盖匹配附件授予 Governance 身份和租户不一致拒绝。
- [x] 已运行定向测试确认当前实现失败。
- [x] 已加入最小绑定逻辑并确认定向测试通过。

### Task 2: 移除独立 managed receiver

**Files:**
- Modify: `cmd/ani-inference-service/app.go`
- Modify: `cmd/ani-inference-service/governance_config.go`
- Modify: `api/inference/v1/conf.proto`
- Modify: `configs/config.yaml`
- Modify: `cmd/ani-inference-service/governance_config_test.go`
- Modify: `cmd/ani-inference-service/governance_tls_test.go`
- Modify: `cmd/ani-inference-service/managed_process_test.go`
- Generate: `api/inference/v1/conf.pb.go`

- [x] 删除独立监听器创建，退款客户端不再要求 receiver 配置。
- [x] 删除 Bootstrap 中 receiver 配置并通过固定生成流程更新代码。
- [x] 将身份与端口测试改为普通 `19090` 入口。

### Task 3: Helm 与文档边界

**Files:**
- Modify: `charts/ani-inference/values.yaml`
- Modify: `charts/ani-inference/templates/deployment.yaml`
- Modify: `docs/contracts/managed-gpu-owner-handoff.md`
- Modify: `docs/specs/inference-service.md`

- [x] 明确 Chart 只配置 `19090/19091`，删除 managed receiver 的部署说明。
- [x] 保留 Governance 退款客户端的 mTLS 配置说明。

### Task 4: 全量验证与提交

- [x] 运行 `go test -count=1 ./...`、`go vet ./...`、`go build -trimpath ./...`。
- [x] 运行 Helm lint/template 并检查 Service 只暴露 `19090/19091`。
- [x] 检查差异和敏感信息后提交并推送 Inference。
