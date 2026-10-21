# Inference API managed compute contract

The API module is `github.com/liangzai006/ani-inference-service/api`.
Managed compute requires this module and a matching owner runtime. The existing
published `api/v0.1.1` is still pinned by the Inference root module. The package
split described below is a working-tree change pending a new immutable release.

## Package ownership

- `inference/v1`: generated public protobuf messages and gRPC clients/servers,
  including `ModelReferenceService`.
- `inference/contract/v1` (`contractv1`): shared command validation, normalization
  and canonical digests. Governance and Inference must use matching semantics.
- `inference/crd/v1`: public Kubernetes resource types; unchanged in this split.
- `internal/conf` in the service root: private Bootstrap/server/refund wiring and
  configuration validation. It is no longer part of this public module.

RPC-only clients import `inference/v1`. Callers that validate or hash commands
also import `inference/contract/v1`:

```go
import (
    contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
    inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func validate(req *inferencev1.CreateInferenceServiceRequest) error {
    return contractv1.ValidateManagedCreate(req)
}
```

The RPC package no longer imports Kubernetes or Accelerator's `gpuprotocol`
validation package. Accelerator **generated protobuf types** remain necessary
because the wire schema references them. Module-level requirements on
Kubernetes and Accelerator remain for CRD/contract consumers; this is a Go
package split, not independent module versioning.

## Release migration

This removes exported Go helpers and configuration types from `inference/v1`.
It is a breaking Go package change, even though the public RPC schema and digest
bytes are unchanged. Do not overwrite published tags or silently upgrade
consumers without migrating imports.

Until the new API is published, the root service keeps its immutable API pin
and existing helper imports; its configuration wiring already uses local
`internal/conf`. The release import patch and isolated compatibility check are
described in [the migration record](../docs/execution/records/2026-10-21-inference-api-boundaries.md).
No local `replace` or `go.work` is committed for normal builds.

## Shared command semantics

The helpers below now belong to `contractv1`; request/response types remain in
`inferencev1`. Their algorithms, schemas and fixed-vector tests are preserved.

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

## Generation and verification

From the repository root, set `BUF` to an absolute path to the pinned Buf
v1.60.0 binary, then run:

```bash
scripts/generate-api.sh generate  # Public RPCs and internal configuration
scripts/generate-api.sh check     # Compare regenerated output without writing
scripts/verify-api.sh             # API tests, vet, build and dependency boundaries
python3 scripts/verify-api-compatibility.py  # Pending imports + local API, isolated
GOWORK=off go test ./...           # Runtime using published API dependencies
```

Generation verifies Buf build provenance and uses the locked Accelerator
module for proto input. Public output uses `buf.gen.yaml`; private configuration
uses `buf.config.gen.yaml`. The Go plugins remain pinned to v1.36.11 and v1.6.0.
The private configuration namespace is `inference.config.v1` so it can coexist
with the old published configuration descriptor during migration. Existing
YAML fields and environment substitution are unchanged.

API tests can also run from this directory with `GOWORK=off go test ./...`.
The compatibility check stages tracked and untracked source files, applies the
consumer import patch and replaces the API module only inside a temporary
copy. It tests the pending release without changing the real root dependencies.
A successful local check is not evidence of API publication or deployment.
