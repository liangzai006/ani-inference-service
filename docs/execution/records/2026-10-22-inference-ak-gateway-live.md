# 动态 Inference AK 网关实测

日期：2026-10-22。目标 context：`kubernetes-admin@kubekey`。
Inference 分支：`codex/inference-ak-api-20261022`；本轮只提交本地，不 push Git。

## 部署与修复

恢复时 Inference Helm revision 44 已部署，主容器为
`docker.changqingyun.cn/ani/inference-service:ak-authz-20261022-r1`，companion 为
`docker.changqingyun.cn/ani/inference-gateway-authz:ak-authz-20261022`，Pod `2/2 Running`，
客户端 Certificate Ready。本轮没有重建这两个业务镜像或升级 Helm。

1. 修复 WasmPlugin CRD 层级：`match_type`/`match_list` 放入 `spec.defaultConfig`。
   规则只包含 `/v1/` 前缀和 `/v1` 精确匹配，不包含管理路径。
2. 内部 registry 缺少 ext-auth，官方 OCI 镜像通过 `oras cp` 完整同步，固定 manifest
   `sha256:35abb074337b06c67d47b1a4da43422657dec8b7853a602e144eea0366174229`。
   Envoy ECDS config dump 确认加载了对应的 Wasm 文件与配置。
3. 仅有 Kubernetes Service 时，Envoy 没有 authz cluster，插件请求报 `bad argument`
   并 fail-close 503。向现有 McpBridge 增量注册私有 DNS 来源，插件改用
   `ani-inference-authz.dns:8080`；Envoy cluster 出现并报告 healthy。
   `register-authz.sh` 使用 resourceVersion 条件更新，保留其他服务来源；二次运行无变化。
4. 独立 `ext-auth.yaml` 显式启用动态认证；公共 `plugins.yaml` 保留基础插件，避免
   基础安装尚无 authz companion 时启用该依赖。部署说明与 Chart 校验同步更新。
5. 确认 ext-auth 已加载且 scope 拒绝生效后，删除旧
   `higress-system/ani-inference-key-auth` WasmPlugin 和同命名空间的
   `ani-inference-model-router` Ingress。默认 landing Ingress 与禁用的
   `key-auth.internal` 保留。运行时维护的 Gateway API bootstrap HTTPRoute 继续有效。

旧对象备份、mTLS 凭据和一次性 secret 只保存在权限受限的临时目录，不入 Git；
没有把动态 secret 写入 Kubernetes 对象。没有修改 Governance 源码或部署。

## 实际验证

通过既有租户验证账号登录 Governance。现场运行的 Governance 镜像为
`main-3806ae5`，创建/删除使用其已部署的顶层 JSON 合同；工作区内新的
`preset_id`/compute-preset 合同尚未部署，本轮没有迁移该合同。

经 BFF 创建临时 CPU vLLM 服务：

- served model：`ak-dynamic-e2e-20261022`。
- service ID：`11cba09c-7179-42e1-976d-da7b9fe61fb6`。
- create operation：`349ef507-cf8d-4a34-8f20-2e952a9fa8d3`，最终 `succeeded/complete`。
- Model version：`9369a759-f1a4-48fd-9c48-1f727ba7459e`，Qwen3-0.6B，3 CPU / 8 GiB。
- predictor `1/1 Running`；业务详情 `ready/published`，`health=unknown` 单独保留。
- 模型 HTTPRoute `ani-pub-8937dd65fd46da597865` 的 parent 为
  `Accepted=True/ResolvedRefs=True`，上游请求过滤器移除 `x-api-key`。

临时动态 key 由 Governance mTLS 身份调用真实 `CreateInferenceAccessKey` 签发，
主验证 key 与模型所属租户一致，限制 served model、设置一小时过期。

| 场景 | 结果 |
| --- | --- |
| 动态 key + JSON model，未手填内部模型头 | `/v1/completions` 200，非空生成文本 |
| 无 key / 无效 key / 已淘汰静态 key | 401 |
| key 的模型 scope 不匹配 | 403 |
| 禁止模型 + 伪造允许模型内部 Header | 403，不能绕过 model-router |
| 单 key RPM=2，连续第三个请求 | 429，带 `Retry-After`；前两次发生于模型路由未就绪时，只证明鉴权通过，非生成成功 |
| 临时将 ext-auth endpoint 指向不可用端口 | 503；恢复清单后无 key 再次返回 401 |
| 轮换主 key 后使用旧 secret | 401 |
| 轮换后的新 secret 调用真实模型 | 200 |
| 撤销后使用新 secret | 401 |
| Governance `/api/v1/inference/services` | JWT 管理列表 200 |
| Higress `/api/v1/inference/services`，有/无 key | 均为 404，无管理 API 路由 |

生成 ID：`cmpl-1ffee32f-de22-40f1-923e-33018af6d17c`，输出
`Paris. The capital of Italy is Rome. The capital of`；5 prompt tokens、12 completion tokens。
轮换后生成 ID：`cmpl-4b366777-f596-4ce5-b89b-919e1f080004`。

原始证据目录：`/tmp/ani-ak-e2e-20261022/`，目录权限 0700。secret、客户端私钥、
会话文件权限 0600；网关结果记录不包含 key。所有本轮临时动态 key 均已撤销。

## 清理

经 Governance 的 `POST /api/v1/inference/services/{id}:delete` 提交删除，operation
`3660ef4b-7928-4bfb-ad4e-5c850e0d5a39` 最终 `succeeded/complete`。
本轮服务的 ANI/KServe CR、predictor Pod、Deployment/ReplicaSet、Service、
下载 Job/Secret、PVC、模型 HTTPRoute 与 Volcano PodGroup 均已回收；
租户管理列表恢复为空。既有模型版本和其他任务的 CR 未删除。

## 检查与边界

- 核心插件与动态认证清单的 Kubernetes server-side dry-run 通过。
- `bash charts/verify.sh`、`bash scripts/preflight_test.sh`、注册脚本语法与幂等实测通过。
- `go test -count=1` 定向覆盖 gatewayauthz、inferenceaccess、Kubernetes adapter；
  authz command package 编译通过（无测试文件）。
- `git diff --check` 通过。Inference 仓库没有 Makefile，本轮未修改 Proto 或生成输出。
- 服务级可信 route scope、租户聚合/模型全局限流、Redis 和跨网关一致性仍属后续切片。
- 本轮为真实 CPU 单模型生成；不把它扩展为 GPU、多模型或跨网关验证。
