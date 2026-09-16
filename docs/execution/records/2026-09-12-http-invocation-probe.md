# 2026-09-12 HTTP invocation probe adapter

## 实现

新增 `internal/data/invocation.HTTPProbe`。它只接受显式
`EndpointResolver`，不会从镜像、Service 名称或默认端口推断地址。HTTP 2xx
返回 `Known=true, Healthy=true`；非 2xx 返回 `Known=true, Healthy=false`；
连接/传输失败返回 `Known=false`，避免把暂时不可达误记成稳定不健康。未传入
HTTP client 时使用 5 秒超时。

该 adapter 尚未接入主进程，因为 Service/publication endpoint 合同仍未冻结。

## 证据

- httptest 验证 2xx、503、传输失败和缺少 resolver 四种语义。
- 开发机 loopback 测试通过；普通沙箱无 loopback 权限时测试明确跳过。
- 真实推理引擎、发布地址、鉴权和数据面协议仍为 `not_verified`。
