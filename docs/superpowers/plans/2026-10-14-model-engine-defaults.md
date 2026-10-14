# Model 版本默认启动配置联动实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 验证 Model 提供前端回填的镜像、引擎类型、完整启动命令，确保创建推理保留调用方最终选择。

**Architecture:** 复用 Model Get/List 版本接口和 Gov 现有字段投影。Inference 只从 Model 取得制品信息，最终启动配置仍从创建请求取得；不增加无消费者的快照字段或服务端默认命令。

**Tech Stack:** Go、现有 protobuf/gRPC 接口、PostgreSQL/sqlc、现有 Go 测试工具。

## Global Constraints

- 完整命令按字节传递，不拼接、拆分或改写。
- Model 字段只供前端回填；创建请求必须携带最终引擎类型、镜像和命令。
- 本次不修改 Gov 仓库，不提交、不推送 Gov。
- 不改 proto 格式、不增加环境变量、不修改集群。
- 本计划完成后分别在 Model 和 Inference 仓库创建本地提交；不提交或修改 Governance 仓库。

---

### Task 1: 核验 Model 版本默认字段

**Files:**
- Modify: `/root/kubercon/ani-model-service/internal/service/model_test.go`
- Modify: `/root/kubercon/ani-model-service/internal/data/postgres/artifact_integration_test.go`

**Interfaces:**
- Consumes: `CreateModelVersion`、`GetModelVersion`、`ListModelVersions` 和现有 VersionStore。
- Produces: 覆盖真实 handler 和持久化边界的回归测试，无新增业务接口。

- [x] 将已有 `TestCreateAndGetModelVersionPreservesEngineDefaults` 从直接调用转换函数改为调用真实 `GetModelVersion`；断言创建和查询都保留三个字段。
- [x] 使用隔离版本列表 stub 检查 `ListModelVersions` 返回每个版本自己的默认值，并校验选择一个版本的临时修改不会影响其他版本。
- [x] 在现有 PostgreSQL 版本读写测试中设置并断言三个字段：

```go
EngineType: "custom-runtime",
EngineImage: "registry.example/engine:v1",
StartupCommand: "exec  custom-server '/models' --served-model-name \"my-model\"",
```

- [x] 运行 `GOWORK=off go test ./internal/service ./internal/data/postgres -count=1`；数据库集成测试在未设置 `MODEL_DATABASE_URL` 时跳过。
- [x] 运行 `GOWORK=off make verify`，退出码为 0。

### Task 2: 完整请求校验及临时覆盖

**Files:**
- Modify: `/root/kubercon/ani-inference-service/internal/data/model/create.go`
- Modify: `/root/kubercon/ani-inference-service/internal/data/model/create_test.go`

**Interfaces:**
- Consumes: `CreateInput`、`Client.GetReadyVersion`。
- Produces: 拒绝缺失镜像的内部创建输入；完整输入按原值传给已有持久化用例。

- [x] 在现有缺失启动配置测试中加入仅缺镜像、类型或命令的输入（分别覆盖空串和全空格），期待 `codes.InvalidArgument` 且 Model 查询/持久化未发生。
- [x] 运行 `GOWORK=off go test ./internal/data/model -run TestCreateRequiresCallerEngineConfiguration -count=1`，确认缺镜像场景失败。
- [x] 补齐已有校验：

```go
if strings.TrimSpace(in.EngineRuntime) == "" || strings.TrimSpace(in.ImageRef) == "" || strings.TrimSpace(in.EngineCommand) == "" {
    return nil, status.Error(codes.InvalidArgument, "engine type, image and startup command are required")
}
```

- [x] 修正未就绪和制品冲突测试的前置输入：提供完整引擎配置、断言具体状态码及已调用 Model，保证没有被缺字段提前挡住。
- [x] 在覆盖测试中传入与 Model 不同的类型/镜像/完整命令，断言三个字段原样到达持久化用例且 Model 默认值未被修改。
- [x] 运行 `GOWORK=off go test -p 1 ./...`、`go vet -p 1 ./...`、`go build -p 1 ./...`。

### Task 3: 前端对接说明及审查

**Files:**
- Modify: `/root/kubercon/ani-inference-service/internal/data/model/README.md`
- Create: `/root/kubercon/ani-model-service/docs/model-inference-fields.md`
- Modify: `/root/kubercon/ani-model-service/README.md`

- [x] 以中文说明现有 Gov Get/List 路径和字段映射：`engine_type → engine_type`、`engine_image → engine_image`、`startup_command → command`；展示扁平创建请求，包含 CPU/内存/实例数。
- [x] 说明字段保存在 ModelVersion，历史空字段需由用户填写，修改推理表单不更新模型默认值；后端已有，前端表单回填仍需消费这些字段。
- [x] 修正 Inference README 中“仅兼容旧契约”的过时表述，保留不从 Model 自动填命令的约束。
- [x] 运行两仓库 `git diff --check`，审查 diff 和验证证据；未做集群或真实前端验证。
