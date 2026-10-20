# Inference API managed compute contract

The API module is `github.com/liangzai006/ani-inference-service/api`.
Managed compute requires this module and a matching owner runtime. The
`api/v0.1.1` candidate adds the contract below; a candidate name is not evidence
that the immutable version has been published or can be downloaded.

`CommandContext` identifies both managed CPU and managed GPU resources.
`ValidateManagedCreate` checks the fixed identity, exact CPU/RAM resource
snapshot and complete GPU attachment where applicable. CPU carries no GPU
request, execution, display or cluster. Model artifact metadata is resolved by
the owner, not accepted from Governance as artifact authority. Quota charges
remain exclusively in Governance.

`NormalizeTopology(runtime, replicas, devicesPerUnit)` is the authoritative
mapping to execution container count/name and total device count. Zero devices
means CPU. Deployment GPU uses `kserve-container`, whole GPU may use multiple
devices per container, shared GPU requires one, and GPU LWS is rejected. CPU
LWS remains supported. Invalid shapes and count overflow are rejected before
quota acceptance.

The new `ManagedCreateCommandHash`, `ManagedDeleteCommandHash` and
`ManagedUpdateCommandHash` use separate `gov-inference-*-v2` schemas, the existing
`acc-c14n-v1\n` hash prefix, lexical JSON object keys and decimal strings for
integer fields. CREATE binds tenant, current actor, owner/action, fixed
resource/CREATE IDs and the immutable business payload. DELETE binds the new
DELETE ID, original CREATE ID and `ManagedDeleteScope` at wire field 8. The
scope contains CPU/GPU class, original GPU cluster (empty for CPU), and original
CREATE command digest. Resource classification is never inferred from empty
charges. A new DELETE actor may differ from the creator.

Correlation request IDs and the digest field itself are excluded from CREATE
and DELETE command hashing. Set the context identity, calculate the command
digest, then attach it and run the complete validation. The old
`Managed*RequestHash` helpers retain their published GPU-v1 byte semantics for
explicit historical readers; they do not enable new managed CPU requests.

`ValidateUpdatePatch` accepts only nonempty masks containing
`model_version_id`, `served_model_name`, and whole `engine`. Selected fields
must be nonempty; engine requires type, image and the complete command. Frozen
resource/topology/provider/endpoint/artifact attachments are rejected even
outside the mask. Unselected business fields are ignored and must not be
merged by the runtime. The patch digest sorts mask paths, binds tenant/current
actor/resource/expected generation, and hashes only selected business fields.
The owner reads the persisted generation and merges the patch under its
existing generation and idempotency transaction.

Generate from the repository root with `scripts/generate-api.sh generate`.
It verifies Buf v1.60.0 build provenance and uses the locked published
Accelerator module for Proto input. The generation template pins both Go
plugins. Run API tests from this directory with `GOWORK=off go test ./...`.
Generation, dependency download and tests for this delivery run on the
designated Fedora host only. Root consumers must wait for an immutable API
release; local replacements, `go.work` and copied unpublished SDKs are not
supported.
