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
existing PostgreSQL spec. `RuntimeRouter.PullSecrets` synchronizes their
shared pull Secret before apply and running reconciliation. Stop/delete never
fetches registry credentials or deletes this shared object. The only writer
is `SharedPullSecrets`, which verifies tenant, namespace, registry, ImageSpace,
managed-by, credential generation and resourceVersion. It rejects foreign or
same-generation inconsistent objects and uses optimistic updates for a new
credential generation. No individual service owner reference is attached.

Deployment, LWS and KServe predictor/leader/worker templates explicitly refer
to that Secret and constrain OS/architecture from the accepted resolution.
The runtime ServiceAccount keeps its existing no-token permissions; system
materializer image credentials remain independent. KServe observation checks
that generated child Pods preserve the accepted Secret reference. GPU LWS
retains its existing unsupported boundary. Historical raw-image generations
have no fabricated catalog selection and keep their accepted runtime data.
