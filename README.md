# ani-inference-service

独立 Inference 服务仓库。当前包含 Kratos gRPC/layout 骨架、独立 PostgreSQL 持久化、typed CRD/Controller 适配和分阶段设计；不依赖 ANI monorepo 运行时。

完整文档入口：[docs/START-HERE.md](docs/START-HERE.md)。当前状态：[docs/execution/status.md](docs/execution/status.md)。

## 安全检查

安全扫描工具安装在仓库本地 `.tools/bin`，不会成为运行时依赖：

```bash
bash scripts/security-audit.sh secrets
bash scripts/security-audit.sh vuln
# 两项一起执行
bash scripts/security-audit.sh audit
```

脚本固定使用 `govulncheck v1.7.0` 和 `gitleaks v8.30.1`。漏洞扫描返回非零时表示发现
需要处理的依赖或 Go 工具链漏洞；密钥扫描返回非零时表示发现敏感信息。

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
ANI_MODEL_MATERIALIZER_IMAGE=registry.example/model-fetcher:v1.2.3  # required
ANI_MODEL_STORAGE_CLASS=  # optional; empty uses the cluster default StorageClass
ANI_MODEL_DOWNLOAD_CA_CONFIGMAP=  # optional; trusted CA bundle for HTTPS artifact downloads
ANI_MODEL_DOWNLOAD_CA_KEY=ca-certificates.crt  # ConfigMap data key; read-only in download Jobs
```

GPU resolution is opt-in per request. A request without `resource.gpu` does
not call the Accelerator service and does not add GPU scheduling fields. Only
when that nested object is supplied may the following optional mTLS settings
be configured:

```text
ANI_ACCELERATOR_GRPC_ADDR=accelerator.<namespace>.svc:9000
ANI_ACCELERATOR_CLUSTER_ID=<new-cluster-uuid>
ANI_ACCELERATOR_CA_FILE=/var/run/secrets/accelerator/ca.crt
ANI_ACCELERATOR_CERT_FILE=/var/run/secrets/accelerator/tls.crt
ANI_ACCELERATOR_KEY_FILE=/var/run/secrets/accelerator/tls.key
ANI_ACCELERATOR_SERVER_NAME=<accelerator-service-tls-name>
```

The client requires TLS 1.3 and mTLS; it has no plaintext fallback. The
Accelerator server must grant the Inference identity `ResolveGpuRequest` before
these settings are enabled for real GPU operations.

The returned URL is `${ANI_HIGRESS_PUBLIC_BASE_URL}/v1/completions`. The
HTTPRoute matches the configured path and the exact
`x-higress-llm-model=<served-model-name>` header:

```bash
curl -H 'x-higress-llm-model: <served-model-name>' http://10.10.1.67:30090/v1/completions
```

Set `ANI_HIGRESS_PUBLIC_BASE_URL=https://models.example.com` when a DNS name
points to the Higress public address. The model header distinguishes multiple
models sharing one Gateway. New inference generations use
`runtime.provider: "kserve"`; an update that omits the provider inherits the
existing generation. The deployment adapter is retained only to remove and
observe legacy Deployment generations; new requests target the KServe
predictor or LLM workload Service.
The Publication adapter never calls the Higress admin API directly.

When Kubernetes lifecycle is enabled, the Model client resolves the immutable
`model_version_id` and obtains a short-lived HTTP or HTTPS download URL. The materializer
creates a per-service/generation PVC and fetch Job, verifies the artifact SHA256,
and only then allows the vLLM runtime and Higress publication to proceed. The
fetcher image must contain Python 3 and the standard library; it does not receive
long-lived Model or Storage credentials.
