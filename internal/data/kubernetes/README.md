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
