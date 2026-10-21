#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOWORK=off

./scripts/generate-api.sh check
go -C api mod tidy -diff
go -C api test -count=1 ./...
go -C api vet ./...
go -C api build ./...

# A reference-query client must not compile command validation or Kubernetes.
# Accelerator protobuf types are still part of the public inference RPC schema.
dependencies=$(go -C api list -deps ./inference/v1)
if printf '%s\n' "$dependencies" | grep -E '(^k8s.io/|/pkg/gpuprotocol$|/inference/contract/|/inference/crd/)'; then
  echo 'RPC package imports command validation or Kubernetes code' >&2
  exit 1
fi
if find api -name conf.proto -o -name conf.pb.go | grep .; then
  echo 'runtime configuration must stay outside the public API module' >&2
  exit 1
fi
