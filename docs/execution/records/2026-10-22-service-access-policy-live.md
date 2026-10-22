# 2026-10-22 服务访问策略验收

## 范围与部署

服务策略直接绑定服务 ID，提供查询、完整替换、租户白名单和按调用租户共享的
服务 RPM/RPH；同时执行动态 AK 的有效性和服务/模型 scope。

- 集群：`kubernetes-admin@kubekey`。
- Inference Helm revision 45，镜像
  `docker.changqingyun.cn/ani/inference-service:service-policy-20261022`，迁移 ledger
  已记录 `000023_inference_service_policies.sql`；companion 保持原动态 AK 版本。
- Governance Helm revision 36，镜像
  `docker.changqingyun.cn/ani/governance:service-policy-20261022-r2`。
  部署源码基于现场 `3806ae5` 的独立策略分支；没有发布主工作区的算力规格创建迁移。
- 入口分别验证：Governance `192.168.102.68:32248`，Higress
  `192.168.102.68:30090`。

API dry-run 只新增 GET/PUT 两条 policy 路由，随后显式同步并登记权限关系。
仅对验证租户 26 的现有 `tenant:manager` 角色 34 增加 policy read/update，要求
既有 INFERENCE 套餐；未对其他角色或共享套餐扩权。

## 真实业务结果

临时服务 `24173ab1-1d3a-4fa2-80e2-bb4db351c792` 使用已导入 Qwen3-0.6B 和 CPU
vLLM。CREATE operation `484f3496-b929-4aff-87ae-174105815dbf` 最终 succeeded。
以下 200 推理均检查了 choices 中的非空生成文本，不能仅凭状态码视为通过。

| 场景 | 结果 |
| --- | --- |
| Governance 列表与策略查询 | 200，默认白名单仅所属租户 |
| 未登录查询策略 | 401 |
| 策略路由尚未完成权限登记 | 403，保持 fail-closed |
| 保存空白名单再查询 | 200，明确保存为空 |
| 保存负数额度 | 400，旧策略保持 |
| Higress 请求管理 policy 路径 | 404 |
| 所属租户、仅 service scope 的有效 AK | 200，非空生成 |
| 未授权的其他租户 AK | 403 |
| 白名单加入调用租户后立即请求 | 200，非空生成 |
| 同一调用租户的两把 AK，服务 RPM=2 | 前两次 200，第三次 429 + Retry-After |
| 同窗口降低 RPM=1 | 429，未清空计数 |
| 同窗口提高 RPM=4 | 下一次 200 |
| 移除调用租户 | 403 |
| 已移除租户伪造 tenant/service header | 403 |
| 空白名单下所属租户请求 | 403 |
| 恢复所属租户后请求 | 200，非空生成 |
| 缺失/无效 AK | 分别 401 |
| 最终部署版本的 GET 伪造身份 header | 400 |

网关业务断言共 14 项通过。小时限制、并发额度、审计失败回滚及同名模型歧义
在独占 PostgreSQL 的真实 Repository/Service 测试验证，本次现场没有额外执行
小时边界等待或同名服务部署。

## 代码验证

- Inference 全量 `go test ./...`、`go vet ./...`、`go build ./...` 通过。
  普通全量测试中需要数据库的既有用例未配置 DSN 时会 skip；本次另以独占库
  显式运行 `TestServicePolicyIntegration` 和 `TestInferenceAccessKeyRepositoryIntegration`，
  覆盖真实数据库与事务行为。
- Helm lint/template、迁移副本一致性和 preflight 脚本测试通过。
- Governance `make api`、service/data/server/constants 测试、相关 vet/build、
  API catalog 和 admin CLI 测试通过；运行基线分支也单独生成、测试并构建。
- Console Oxlint、全仓格式、格式检查和 TypeScript 检查通过。用户明确授权本次
  使用本地源码替代不可用的 GitNexus；未运行生产构建、未部署 Console、未执行
  浏览器交互验收。策略 Tab 的查询、编辑和缓存刷新已实现。

## 清理

三把现场测试 AK 均已撤销。DELETE operation
`31e2610f-c002-4883-a9ce-1dfe72137388` succeeded；测试服务的运行 Pod、Deployment、
Service、PVC、Job、HTTPRoute、KServe CR 和 PodGroup 均不再存在，列表不再返回
测试资源，policy 查询为 404。详情仍按现有合同返回 deleted/withdrawn 墓碑，
没有为了清理而物理删除历史与审计。

临时客户端证书/私钥、登录 token、原始 AK 和提取的登录候选均已删除，两个
port-forward 已停止。未改动无关服务和原有 Console 工作区内容，未执行 Git push。
