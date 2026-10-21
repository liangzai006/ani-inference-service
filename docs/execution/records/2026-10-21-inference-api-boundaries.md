# Inference API 配置与公共契约拆分

本轮仅处理运行时配置归属、公共 RPC 包与契约工具的依赖分离。
CRD 类型与 DeepCopy 文件保持原样，不将其已有缺陷算作本轮修复。

## 源码变化

- `api/inference/v1` 只保留公开 protobuf 消息与 gRPC 接口。
- `internal/conf` 接管 Bootstrap、监听器、Governance 退款连接配置及校验。
  启动入口、服务装配和对应测试已切换；YAML 字段和环境变量约定不变。
- `api/inference/contract/v1` 接管公共校验、拓扑规范化与摘要计算。
  原有固定向量与负向测试一起迁移，算法及公共 RPC schema 不变。
- 配置 protobuf 使用独立的 `inference.config.v1` 名称空间，避免与旧发布包
  中的 `inference.v1.Bootstrap` 在过渡阶段冲突。生成输出通过固定工具生成。
- RPC 包的依赖闭包不再包含 Kubernetes、`gpuprotocol`、CRD 或契约工具。
  Accelerator 生成的协议类型仍是 wire schema 的必要依赖。API 模块仍需
  为 CRD/契约工具声明 Kubernetes 和 Accelerator 版本，没有拆成多个模块。

## 发布前与发布后的消费方式

根模块仍固定 Inference API v0.1.1，公共校验调用仍使用该发布包的旧入口，
以保持普通构建可用。配置已经迁到根模块，不依赖新 API 发布。
本地 API 新源码移除了旧 helper 导出，属于 Go API 不兼容改动，不能仅提升
版本而不迁移消费者。也不能通过永久 replace/go.work 把本地结果当成已发布。

发布清理后的不可变 API 版本后：

1. Inference 应用
   [调用方迁移补丁](../patches/2026-10-21-inference-contract-consumers.patch)，
   并更新根 go.mod 的 API 固定版本。该补丁已在隔离副本中联同新 API 验证。
2. Governance 的请求校验、创建/删除摘要和用量恢复解码也需切换到
   `contractv1`。已准备
   [Governance 迁移补丁](../patches/2026-10-21-governance-contract-consumers.patch)，
   相对路径以 Governance 根目录为准。本轮未应用或测试该跨仓补丁；
   更新其固定 API 版本时必须进行对应业务测试。
3. Model 保持原来的 `api/inference/v1` RPC 客户端导入，只需更新 API 版本。
4. 重新构建镜像并分别验证管理与推理入口。此记录不代表发布或部署完成。

## 验证

- `scripts/verify-api.sh`：固定工具重新生成无差异；API 测试、vet、构建、
  tidy 检查与依赖边界检查通过。
- 根模块 `go test -count=1 ./...`、`go vet ./...`、`go build ./...`、
  `go mod tidy -diff` 通过，使用真实已发布 API 依赖。
- `python3 scripts/verify-api-compatibility.py`：复制当前源码到临时目录，
  应用 Inference 导入迁移补丁，仅在临时目录替换 API 依赖，然后运行根模块
  测试、vet 和构建，全部通过。不会修改当前仓库依赖。
- Model 使用临时 modfile 指向本地新版 Inference API，启动组合根与引用
  查询客户端测试通过；Model 仓库的固定依赖未被本轮改写。
- 新增配置行为测试覆盖现有 YAML 加载、ANI_ 环境变量、监听器及超时校验。
  测试首次使用未加 ANI_ 前缀的变量而失败；核对生产配置源后修正测试输入，
  未修改生产配置解析逻辑。
- 环境依赖型测试仍遵守原有显式启用条件；普通 Go 测试通过不等于数据库、
  Kubernetes 或跨服务真实环境联调通过。本轮没有部署或数据库变更。

## DeepCopy 问题说明

标准 controller-gen 会为 ListMeta 执行深拷贝，并在 DeepCopyObject 中判断
空指针后返回 nil。历史 `progress.md` 记录了 controller-gen v0.11.1 在当前
工具链上失败，因此不能仅根据 `zz_generated.deepcopy.go` 的名字判断产物
已正确生成。该问题留在独立的 CRD 生成修复范围内。
