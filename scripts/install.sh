#!/usr/bin/env bash
set -euo pipefail

# Test/CI-only entry point. Formal deployments must use the Helm and kubectl
# commands in docs/deploy/helm/ani-inference.md. This helper keeps an explicit
# KUBE_CONTEXT so tests cannot accidentally use the operator's current context.
# It installs the Inference-owned platform dependencies first, then the ANI
# Inference service chart, and ends with a read-only preflight.

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

if [[ -z "${KUBE_CONTEXT:-}" ]]; then
  echo "KUBE_CONTEXT is required; refusing to use the current kubectl context" >&2
  exit 2
fi
: "${HIGRESS_PUBLIC_BASE_URL:?HIGRESS_PUBLIC_BASE_URL is required for the Inference chart}"

if [[ "${ANI_NAMESPACE:-ani-system}" != ani-system ]]; then
  echo "ANI_NAMESPACE must be ani-system; refusing a different namespace" >&2
  exit 2
fi
ANI_NAMESPACE=ani-system
HELM_TIMEOUT=${HELM_TIMEOUT:-15m}
KUBE_REQUEST_TIMEOUT=${KUBE_REQUEST_TIMEOUT:-30s}

"$SCRIPT_DIR/install-dependencies.sh"

helm_values=()
if [[ -n "${INFERENCE_VALUES:-}" ]]; then
  helm_values=(--values "$INFERENCE_VALUES")
fi

helm --kube-context "$KUBE_CONTEXT" upgrade --install ani-inference \
  "$REPO_ROOT/charts/ani-inference" \
  --namespace "$ANI_NAMESPACE" --create-namespace \
  "${helm_values[@]}" \
  --set-string higress.publicBaseURL="$HIGRESS_PUBLIC_BASE_URL" \
  --wait --timeout "$HELM_TIMEOUT"

kubectl --context "$KUBE_CONTEXT" --request-timeout "$KUBE_REQUEST_TIMEOUT" \
  -n "$ANI_NAMESPACE" rollout status deployment/ani-inference-service \
  --timeout="$HELM_TIMEOUT"

"$SCRIPT_DIR/preflight.sh"

echo "ANI Inference and its platform dependencies are installed in context $KUBE_CONTEXT."
