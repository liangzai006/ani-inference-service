# 服务访问策略

策略直接绑定 `resource_id`，由 Inference 持有。现有正式 API 模块的
`InferenceServiceManager/GetServicePolicy` 与 `UpdateServicePolicy` 提供查询和
完整替换，不需要发布新的 Proto 字段或新增本地模块替换。

## 管理语义

- 身份来自受信 Governance mTLS 连接的租户、actor 和 request ID。只有所属租户
  可以查询和修改；不存在、已删除或其他租户的服务均返回 NotFound。
- 尚未保存策略时，默认只允许所属租户，服务级额度不限。
- 保存策略会完整替换白名单和额度，同时建立服务绑定；空或省略的白名单拒绝
  所有租户，包括所属租户。移除额度映射表示移除对应服务级限制。
- `allowed_tenants` 必须是非零规范 UUID，重复值归一化；额度映射只允许白名单
  内的租户，RPM/RPH 为非负 int32，0 表示不限。
- `updated_at` 是只读输出。现有合同没有乐观版本前置条件；并发保存按数据库
  服务行锁串行，最后一次完整替换生效。公开 PUT 不接受幂等 token；重复保存
  不会重置计数桶，但会产生新的策略版本和审计事件。
- 策略与审计事件在同一事务提交；审计写入失败时整个替换回滚。

## 推理请求执行

Higress model-router 先从 JSON 解析模型并覆盖 `x-higress-llm-model`；ext-auth
通过现有私有 adapter 和 mTLS `CheckInferenceAccess` 调用 Inference。
客户端提交的 service/tenant header 不参与可信服务选择。

Inference 用数据库的发布事实解析模型对应的服务，要求唯一服务且存在当前
运行 generation 的确认发布。仍可能留有路由的过渡/旧发布同样参与歧义检查；
其他服务共享相同模型名时拒绝。不存在或未发布的模型同样拒绝。由此解析服务级
AK scope，并在扣减事务内再次检查服务、发布和策略。

调用必须同时满足有效 AK、AK 的服务/模型范围和服务的租户白名单。跨租户调用
还须由服务所属租户显式允许调用方租户；管理权限不会因此跨租户开放。

服务限制按 `(所属租户, 服务, 调用租户, 分钟或小时窗口)` 聚合，多把 AK 共享。
UTC 固定窗口和 AK 的秒/分钟/小时限制在同一事务扣减；任一限制失败不消耗
其他桶。新请求超过一个空桶的额度也会拒绝。撤销/轮换使用 key 行锁和版本检查，
避免已经读取的旧凭据在修改提交后继续扣减成功。

修改策略不删除已有窗口计数；降低或提高额度在下一次请求生效。原本不限的维度
不会记录桶，因此首次开启该维度从后续请求开始计数。已有在途推理不被中断。
租户/模型全局限制和独立可复用策略目录不属于此合同。

无效/缺失/撤销/过期 AK 为 401，scope/策略拒绝为 403，额度不足为 429 并返回
Retry-After，鉴权依赖不可用为 503；依赖故障不放行。

## 交付和入口

先显式应用 `000023_inference_service_policies.sql`，再更新 Inference runtime。
Governance 通过 `GET/PUT /api/v1/inference/services/{resource_id}/policy` 提供管理，
权限分别是 `inference:policy:read` / `inference:policy:update`，并要求 INFERENCE
模块资格。必须先 API dry-run、目录同步、权限关联，再对指定租户角色显式授权。
Console 的策略 Tab 提供白名单和额度查询、编辑。

管理入口仅属于 Governance BFF；Higress 只承载 `/v1/...` 推理调用。
