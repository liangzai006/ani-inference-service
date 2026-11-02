# Kubernetes adapter

`controller.go` is the controller-runtime adapter for the `InferenceService`
CRD. It treats CR events and the periodic `RequeueAfter` as wake-ups only;
the domain reconciler remains responsible for reading durable PostgreSQL work,
performing Kubernetes changes, and writing observations. The adapter requires
the CR to carry the service and tenant ownership labels emitted by this
service, and it never adopts an object by name alone.

Runtime-resource watches and UID/resourceVersion binding checks should be added
through explicit handlers when the durable runtime binding repository is
available. They must preserve the same ownership and generation checks.

Catalog image generations carry their accepted `ImageSelection` from the
existing PostgreSQL spec. `RuntimeRouter.PullSecrets` ensures their shared
tenant pull Secret before apply and running reconciliation. Stop/delete never
contacts Resource or deletes this shared object. Resource is the only writer:
`ResourcePullSecrets` only asks `EnsureTenantPullSecret` for the scope
`(tenant, cluster, namespace, registry_authority)`, verifies that the returned
`secret_ref`/`registry_authority`/`generation` match the frozen selection, and
then reads the object through a read-only client. The frozen name must equal
`ani-pull-` + the shared hash; Inference-specific or instance-specific names
are rejected, and Inference holds no Secret write permission.

During running reconciliation, an Ensure `Unavailable` or timeout only blocks
synchronization and runtime writes. The selected provider continues read-only
readiness/invocation observation, and the existing reconciler saves those facts
with its generation and lease fences. Missing objects are reported without
repair or adoption; there is no fallback to the removed local writer. Apply
operations, ownership/material errors and canceled work still fail; recovery
resumes normal synchronization. This does not cache credentials or reduce
refresh frequency.

Deployment, LWS and KServe predictor/leader/worker templates explicitly refer
to that Secret and constrain OS/architecture from the accepted resolution.
The runtime ServiceAccount keeps its existing no-token permissions; system
materializer image credentials remain independent. KServe observation checks
that generated child Pods preserve the accepted Secret reference. GPU LWS
retains its existing unsupported boundary. Historical raw-image generations
have no fabricated catalog selection and keep their accepted runtime data.
