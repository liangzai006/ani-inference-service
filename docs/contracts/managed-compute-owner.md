# Governance managed compute owner

Managed CREATE uses the authenticated Governance identity, trusted tenant and
actor, fixed ResourceID and original CREATE OperationID. `ManagedCommand` is
independent of its optional GPU snapshot. CPU has no GPU cluster, plan, local
reservation, GPU metering marker or accelerator/release RPC. Runtime projections
carry `Managed` and `ManagedGPU` separately. Historical direct records retain
their existing local reservation path.

The independent API module defines the new `gov-inference-create-v2`,
`gov-inference-delete-v2` and `gov-inference-update-v2` hashes. CREATE and DELETE
bind the original identities. DELETE field 8 contains resource class, original
GPU cluster (empty for CPU), and the original CREATE command digest. Old hash
helpers retain their previously published byte semantics.

CREATE checks durable replay and closing before the Model boundary. Final
acceptance repeats the same command/resource locks and writes the complete
service/spec/operation/work plus original response and ACK in one transaction.
A first Model failure has no owner ACK. UPDATE accepts only nonempty masks over
`model_version_id`, `served_model_name`, and the complete `engine`. Off-mask
values do not reach Model or persistence. The locked current generation supplies
all frozen resources, replicas, provider, endpoint and GPU fields. Replays
precede Model and current-state checks; new updates still require running.
Start/Stop/Restart use the current persisted generation and never change Gov's
original acceptance or accounting.

Migration `000021_managed_compute_close.sql` expands the existing managed
records. DELETE-first records no fake service or resource_work. The same
resource lock proves absence of accepted execution facts before CPU is marked
complete at generation 0. Unknown prior execution remains explicitly pending
recovery. GPU persists its empty scope and seal, then the existing `work.Loop`
scans `ListManagedCloseTenants` and claims a short leased close even if CREATE
never returns. A missing absence seal or invalid Pod scope cannot send Gov a report.

Normal GPU deletion retains ObserveAbsence, DeleteCR/CleanupModel and then
release coordination. Runtime deletion uses foreground propagation, and managed
GPU ObserveAbsence reports absent only when the workload objects are gone and no
non-terminal Pod of the service (any generation, Terminating included, plus every
recorded release-scope UID) remains; the CPU-only owned model download Pod is
excluded. Accelerator is not consulted: the sealed runtime absence
and persisted Pod UID scope are the release proof. The coordinator derives a
deterministic evidence ID/digest from them and persists it with the stable
four-field report before its RPC. The event is UUID SHA1 over
`ani-inference/managed-gpu-release/` plus DELETE ID, with reason
`RESOURCE_RELEASED`. Restarted retries reuse the saved report without refreshing
seal/evidence/event; a validated Gov APPLIED/ALREADY_APPLIED receipt is required
before the close completes. Tenant-scoped operation projections show both
DELETE and actually accepted blocked CREATE commands; normal operation rows
win so pages and total remain deduplicated. Query steps distinguish `observe_release`, `recover_execution`, and
`report_resource_released` (valid release evidence and report persisted, owner
notification pending), followed by `succeeded/complete`. Normal DELETE rows
also project the persisted report-pending fact without altering the runner's
stored step. Gov alone supplies refund status. Original ACK replay and current
query progress are separate observations.

Deploy the explicit expansion migration and compatible owner before enabling
the matching Gov producer. Once new records exist, retain a reader and close
recovery implementation that understands them. This change does not deploy,
apply a real business migration, create a model workload or prove physical GPU
release. Formal root-module consumption and software acceptance require the
new API's published immutable version with `GOWORK=off`.

## Catalog image intake

Deploy a compatible receiver before enabling the new producer. Set
`imageRegistry.enabled`, its Resource runtime gRPC address, verified OS/architecture
and a client TLS Secret with the exact identity `ani-inference-service`. The
Resource runtime server identity is `ani-network-service`; this client never
borrows a Governance certificate. The environment equivalents are
`ANI_IMAGE_RUNTIME_GRPC_ADDR`, `ANI_IMAGE_RUNTIME_{CA,CERT,KEY}_FILE`, and
`ANI_IMAGE_TARGET_{OS,ARCH,VARIANT}`. Namespace remains `ANI_INFERENCE_NAMESPACE`.

When enabled, new creates and engine updates require catalog contract version
2. Durable accepted replays run before directory access. First acceptance
resolves outside the database transaction and stores the digest in `ImageRef`,
with selection/platform/Secret reference in existing `spec_json`. An engine
update resolves into a new generation; other updates inherit that frozen spec.
Start/restart and recovery retain the accepted generation's reference. Legacy
raw-image services retain their historical reference and lifecycle semantics;
they are not represented as digest-controlled catalog services. Migrate one by
an explicit complete-engine update with a catalog selection. There is no
automatic lookup or fabricated image ID for old tags.

The sole Secret writer is `SharedPullSecrets`. Each tenant/namespace/registry
scope has its own deterministic Secret; ownership, ImageSpace, registry,
credential generation and resourceVersion are fenced. Runtime Pods reference
it explicitly; the no-token runtime ServiceAccount and system materializer
credentials remain separate. Stop/delete do not call Resource or delete this
shared Secret.

A structured Resource business denial before owner acceptance is returned as
`FailedPrecondition / IMAGE_ADMISSION_REJECTED`, with `ErrorInfo` domain
`ani-inference`, `stage=pre_accept`, original tenant/resource/operation/digest
and a bounded `image_reason`. Generic gRPC failures and unknown results do not
produce this signal. Governance persists the original error and a correlated
independent DELETE, then follows the existing CPU/GPU closing and original
ledger settlement rules. A DELETE ACK alone is not release completion.
