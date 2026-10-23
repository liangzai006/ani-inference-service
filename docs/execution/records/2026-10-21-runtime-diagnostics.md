# Runtime diagnostics owner API

`inference.v1.InferenceDiagnostics` exposes management diagnostics to Governance
through the existing mTLS listener. It is not a public HTTP API; `/api/v1/...`
must continue to go through the Governance BFF. Higress still handles only the
OpenAI-compatible data plane.

## Health semantics

`GetRuntimeDiagnostics` separates the runtime observation timestamp from invocation
health evidence. `unknown` means a ready and published service still has no
conclusive invocation observation, usually because the observation source is
unavailable or the generation changed during the probe. Pod readiness never sets
invocation health. The durable reconciler now records `healthy`/`unhealthy` after
a successful bounded probe; the existing `observed_at` column is the runtime
observation timestamp and is also used for the persisted health observation time.
A diagnostics request may run a fresh probe and returns its own
`health_observed_at`; `observed_at` in that response remains the time of the
diagnostic read.

The durable reconciler now runs the bounded invocation probe after the exact
generation is runtime-ready, published and model-ready. Its result is written
through the existing generation/lease-fenced runtime observation path, so the
service list and detail projections converge without a manual diagnostics read.
The on-demand diagnostics endpoint remains available and keeps its separate
request-time semantics.

## Runtime logs and events

- `GetRuntimeDiagnostics`: owned Pod instances and their containers.
- `GetRuntimeLogs`: timestamped logs by instance, optional container and time range.
- `StreamRuntimeLogs`: live Kubernetes log stream, canceled with the request.
- `ListRuntimeEvents`: event type/time filters applied before pagination and total.

All requests first resolve the service in the authenticated tenant. Kubernetes
namespace comes from deployment configuration. Pods are included only if their UID
or a verified controller owner chain reaches a persisted runtime binding. Labels
alone cannot authorize access, and missing propagated labels do not hide owned
KServe/LWS workers. A replaced parent UID is rejected. The Pod UID is rechecked
immediately after opening logs before emitting content.

Logs come from Kubernetes retained container logs, not a historical archive. The
query cannot restore logs from deleted Pods. Time ranges use an inclusive start
and exclusive end. An omitted container selects the only ordinary container, when
unambiguous. The default tail is 200 lines, maximum 2,000; responses/streams are
bounded to 2 MiB, individual log lines to 256 KiB. Live streams last at most five
minutes (or the supplied end time) and at most 32 can run in one process. A stream
that reaches its byte budget ends with ResourceExhausted, so truncation is visible.

Events are Kubernetes retained events for bound objects and owned Pods, sorted by
last occurrence then ID, newest first. Aggregated event count is preserved.
Pagination defaults to page 1, size 20, maximum size 100. Scans are bounded to 500
Pods in the configured namespace and 5,000 events; exceeding a bound returns an
error instead of a misleading partial total. Kubernetes event retention limits
historical coverage. Sources that fail return an error, never fabricated empty
successes.

Streaming RPCs have explicit certificate/method policy and tenant/actor validation;
unary middleware alone does not protect gRPC streaming methods. Namespace RBAC adds
read access to Pod logs, events and the ReplicaSet/StatefulSet ancestors needed to
verify ownership.

## Validation and release boundary

Pinned protobuf generation, standalone API verification and isolated runtime
compatibility checks validate this local change. Runtime verification stages the
unpublished local API and applies the existing contract-import migration patch;
no permanent `replace` or dependency version change is made. The current published
API version does not contain these new RPCs. Publish/adopt the new API version
before a normal root build or production image build. No cluster deployment or
live invocation validation is claimed by these source/test changes.
