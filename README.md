# ani-inference-service

独立 Inference 服务仓库。当前包含 Kratos gRPC/layout 骨架、独立 PostgreSQL 持久化、typed CRD/Controller 适配和分阶段设计；不依赖 ANI monorepo 运行时。

完整文档入口：[docs/START-HERE.md](docs/START-HERE.md)。当前状态：[docs/execution/status.md](docs/execution/status.md)。

## 文档

设计导航、规格、ADR、计划和执行记录均位于 `docs/`；领域词汇位于仓库根目录 `CONTEXT.md`。

规格标识：`确认` 指已明确方向；`草案` 指待评审设计。文档存在不代表实现、测试或部署通过；实际状态只看 `docs/execution/status.md`。

## Higress Publication

The operation runner wires the production Publication port to a Kubernetes
Gateway API `HTTPRoute`. A successful publish server-side-applies a
deterministic route for the target generation; withdraw fenced-deletes it and
waits until the object is absent. Publication confirmation requires the Higress
Gateway parent to report both `Accepted=True` and `ResolvedRefs=True`.

The composition defaults are:

```text
ANI_HIGRESS_GATEWAY_NAMESPACE=higress-system
ANI_HIGRESS_GATEWAY_NAME=ani-higress
ANI_HIGRESS_ROUTE_NAMESPACE=$ANI_INFERENCE_NAMESPACE
ANI_HIGRESS_PUBLIC_BASE_URL=http://10.10.1.67:30090  # required; use the Higress public DNS URL in production
ANI_HIGRESS_PATH_PREFIX=/v1/completions
ANI_MODEL_GRPC_ADDR=ani-model-service.<namespace>.svc:9000  # required with Kubernetes lifecycle
ANI_MODEL_MATERIALIZER_IMAGE=registry.example/model-fetcher@sha256:<digest>  # required
ANI_MODEL_STORAGE_CLASS=cephfs  # default; must support ReadWriteMany
```

The returned URL is `${ANI_HIGRESS_PUBLIC_BASE_URL}/v1/completions`. The
HTTPRoute matches the configured path and the exact
`x-higress-llm-model=<served-model-name>` header:

```bash
curl -H 'x-higress-llm-model: <served-model-name>' http://10.10.1.67:30090/v1/completions
```

Set `ANI_HIGRESS_PUBLIC_BASE_URL=https://models.example.com` when a DNS name
points to the Higress public address. The model header distinguishes multiple
models sharing one Gateway. The Publication adapter uses the inference-owned
`-endpoint` Service and never calls the Higress admin API directly.

When Kubernetes lifecycle is enabled, the Model client resolves the immutable
`model_version_id` and obtains a short-lived HTTP or HTTPS download URL. The materializer
creates a per-service/generation PVC and fetch Job, verifies the artifact SHA256,
and only then allows the vLLM runtime and Higress publication to proceed. The
fetcher image must contain Python 3 and the standard library; it does not receive
long-lived Model or Storage credentials.
