# ani-inference-service

独立 Inference 服务仓库。当前包含 Kratos gRPC/layout 骨架、独立 PostgreSQL 持久化、typed CRD/Controller 适配和分阶段设计；不依赖 ANI monorepo 运行时。

完整文档入口：[docs/START-HERE.md](docs/START-HERE.md)。当前状态：[docs/execution/status.md](docs/execution/status.md)。

## 文档

设计导航、规格、ADR、计划和执行记录均位于 `docs/`；领域词汇位于仓库根目录 `CONTEXT.md`。

规格标识：`确认` 指已明确方向；`草案` 指待评审设计。文档存在不代表实现、测试或部署通过；实际状态只看 `docs/execution/status.md`。

## APISIX Publication

When `ANI_KUBERNETES_ENABLED=true`, the operation runner wires the production
Publication port to a Kubernetes Gateway API `HTTPRoute`. A successful publish
server-side-applies a deterministic route for the target generation; withdraw
fenced-deletes it and waits until the object is absent. Publication confirmation
requires the APISIX Gateway parent to report both `Accepted=True` and
`ResolvedRefs=True`.

The composition defaults are:

```text
ANI_APISIX_GATEWAY_NAMESPACE=ingress-apisix
ANI_APISIX_GATEWAY_NAME=ani-apisix
ANI_APISIX_ROUTE_NAMESPACE=$ANI_INFERENCE_NAMESPACE
ANI_APISIX_HOST_SUFFIX=models.example.com  # required; DNS must point to the APISIX public address
ANI_APISIX_PUBLIC_SCHEME=http
ANI_APISIX_PATH_PREFIX=/v1
```

The returned URL is `http://<service-id>.<ANI_APISIX_HOST_SUFFIX>/v1`.
`ANI_APISIX_HOST_SUFFIX` is required when Kubernetes Publication is enabled; its DNS
records must point to the APISIX public address. For a temporary NodePort smoke
test, the host can be supplied as the HTTP `Host` header. The
Publication adapter uses the inference-owned `-endpoint` Service and never
calls the APISIX Admin API directly.
