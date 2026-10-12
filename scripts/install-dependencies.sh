#!/usr/bin/env bash
set -euo pipefail

# Test/CI-only dependency installation helper. Formal deployments must use the
# Helm and kubectl commands in docs/deploy/helm/ani-inference.md. Every test
# cluster operation names its context so the current kubeconfig is never used.
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
source "$SCRIPT_DIR/dependency-versions.env"
: "${KUBE_CONTEXT:?Set KUBE_CONTEXT to the new environment; current-context is never used}"
: "${ANI_HIGRESS_API_KEY:?Set ANI_HIGRESS_API_KEY before installing the gateway}"
for tool in kubectl helm python3; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 127; }
done

ARTIFACTS=${PLATFORM_CHART_DIR:-"$REPO_ROOT/charts/vendor"}
HELM_TIMEOUT=${HELM_TIMEOUT:-15m}
# When running in-cluster (Pod with ServiceAccount), don't use --context
if [ "$KUBE_CONTEXT" = "in-cluster" ]; then
  KUBECTL=(kubectl --request-timeout "${KUBE_REQUEST_TIMEOUT:-30s}")
  HELM=(helm)
else
  KUBECTL=(kubectl --context "$KUBE_CONTEXT" --request-timeout "${KUBE_REQUEST_TIMEOUT:-30s}")
  HELM=(helm --kube-context "$KUBE_CONTEXT")
fi
# Namespace names match the packaged resources. KServe 0.16's config lookup
# uses kserve. Business services and workloads use ani-system.

"$SCRIPT_DIR/fetch-dependencies.sh"

install_chart() {
  local release=$1 chart=$2 namespace=$3
  shift 3
  "${HELM[@]}" upgrade --install "$release" "$chart" \
    --namespace "$namespace" --create-namespace \
    --wait --wait-for-jobs --timeout "$HELM_TIMEOUT" "$@"
}

# Explicit values files allow registries/resources to be set for a new site.
KSERVE_VALUES=${KSERVE_VALUES:-"$REPO_ROOT/charts/platform-values/kserve.yaml"}
LLMISVC_VALUES=${LLMISVC_VALUES:-"$REPO_ROOT/charts/platform-values/llmisvc.yaml"}
LWS_VALUES=${LWS_VALUES:-"$REPO_ROOT/charts/platform-values/lws.yaml"}
CERT_MANAGER_VALUES=${CERT_MANAGER_VALUES:-"$REPO_ROOT/charts/platform-values/cert-manager.yaml"}
HIGRESS_VALUES=${HIGRESS_VALUES:-"$REPO_ROOT/docs/deploy/higress/new-cluster-values.yaml"}
for values in "$KSERVE_VALUES" "$LLMISVC_VALUES" "$LWS_VALUES" "$CERT_MANAGER_VALUES" "$HIGRESS_VALUES"; do
  [[ -f "$values" ]] || { echo "missing values file: $values" >&2; exit 1; }
done

# cert-manager is shared once installed. Adopt a ready installation without
# changing its Helm release. On a completely empty new cluster, install the
# pinned chart; a partial or foreign installation is an error rather than an
# unsafe replacement.
ensure_cert_manager() {
  local crd
  local have_crds=true
  for crd in certificates.cert-manager.io issuers.cert-manager.io clusterissuers.cert-manager.io; do
    if ! "${KUBECTL[@]}" get crd "$crd" -o name >/dev/null 2>&1; then
      have_crds=false
    fi
  done
  local have_deployment=true
  if ! "${KUBECTL[@]}" -n cert-manager get deployment cert-manager -o name >/dev/null 2>&1; then
    have_deployment=false
  fi
  if [[ "$have_crds" == true && "$have_deployment" == true ]]; then
    "${KUBECTL[@]}" wait --for=condition=Established --timeout=30s \
      crd/certificates.cert-manager.io crd/issuers.cert-manager.io crd/clusterissuers.cert-manager.io
    "${KUBECTL[@]}" -n cert-manager rollout status deployment/cert-manager --timeout=60s
    return 0
  fi
  if [[ "$have_crds" == false && "$have_deployment" == false ]]; then
    install_chart cert-manager "$ARTIFACTS/cert-manager-${CERT_MANAGER_VERSION}.tgz" cert-manager \
      --values "$CERT_MANAGER_VALUES" \
      --set crds.enabled=true
    return 0
  fi
  echo "cert-manager is partially installed in context $KUBE_CONTEXT; refusing to replace unknown resources" >&2
  exit 1
}

# KServe owns cluster-scoped CRDs and webhooks. Never install a second
# controller over the KServe instance bundled by another Kubeflow release.
"$SCRIPT_DIR/check-kserve-controller.sh"
ensure_cert_manager

# Official Gateway API and GIE are published as CRD bundles, not Helm charts.
# Server-side apply avoids the annotation size limit of client-side CRD apply.
"${KUBECTL[@]}" apply --server-side --field-manager=ani-inference-platform \
  -f "$ARTIFACTS/gateway-api-${GATEWAY_API_VERSION}-standard-install.yaml" \
  -f "$ARTIFACTS/gateway-api-inference-extension-${GIE_VERSION}-manifests.yaml"
"${KUBECTL[@]}" wait --for=condition=Established --timeout=120s \
  crd/gateways.gateway.networking.k8s.io crd/httproutes.gateway.networking.k8s.io \
  crd/inferencepools.inference.networking.x-k8s.io

install_chart lws "$ARTIFACTS/lws-${LWS_VERSION}.tgz" lws-system --values "$LWS_VALUES"

install_chart higress "$ARTIFACTS/higress-${HIGRESS_VERSION}.tgz" higress-system --values "$HIGRESS_VALUES"
"${KUBECTL[@]}" wait --for=condition=Established --timeout=120s crd/wasmplugins.extensions.higress.io

# Serialize the credential as a JSON string (valid YAML), including quotes or
# backslashes. It is passed on stdin, never as a command argument or log line.
ANI_HIGRESS_API_KEY="$ANI_HIGRESS_API_KEY" python3 - "$REPO_ROOT/docs/deploy/higress/key-auth.yaml.tmpl" <<'PY' | "${KUBECTL[@]}" apply -f -
import json, os, pathlib, sys
payload = pathlib.Path(sys.argv[1]).read_text()
print(payload.replace('"${ANI_HIGRESS_API_KEY}"', json.dumps(os.environ['ANI_HIGRESS_API_KEY'])))
PY
# Initialize the mutable model limit list once. Inference owns it afterwards;
# re-running this installer must not reset the limits of published models.
python3 - "$REPO_ROOT/docs/deploy/higress/plugins.yaml" <<'PY' | "${KUBECTL[@]}" apply --server-side --field-manager=ani-inference-platform -f -
import pathlib, sys
print(pathlib.Path(sys.argv[1]).read_text())
PY
"${KUBECTL[@]}" apply -f "$REPO_ROOT/docs/deploy/higress/gateway.yaml"

install_chart kserve-crd "$ARTIFACTS/kserve-crd-${KSERVE_VERSION}.tgz" kserve
install_chart llmisvc-crd "$ARTIFACTS/llmisvc-crd-${KSERVE_VERSION}.tgz" kserve
"${KUBECTL[@]}" wait --for=condition=Established --timeout=120s \
  crd/inferenceservices.serving.kserve.io crd/llminferenceservices.serving.kserve.io \
  crd/llminferenceserviceconfigs.serving.kserve.io
install_chart kserve "$ARTIFACTS/kserve-${KSERVE_VERSION}.tgz" kserve \
  --values "$KSERVE_VALUES" \
  --post-renderer "$SCRIPT_DIR/drop-kserve-configmap-post-renderer.py" \
  --post-renderer-args "--mode=core"
install_chart llmisvc "$ARTIFACTS/llmisvc-${KSERVE_VERSION}.tgz" kserve \
  --values "$LLMISVC_VALUES" --post-renderer "$SCRIPT_DIR/drop-kserve-configmap-post-renderer.py"
"${KUBECTL[@]}" -n kserve wait --for=condition=Ready --timeout=120s \
  certificate/serving-cert certificate/llmisvc-serving-cert
# These custom resources are installed only after their webhook is running.
install_chart ani-inference-config "$REPO_ROOT/charts/ani-inference-config" kserve

"${KUBECTL[@]}" create namespace ani-system --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -
"${KUBECTL[@]}" label namespace ani-system ani.liangzai006.io/environment=new-cluster --overwrite
"${KUBECTL[@]}" wait --for=condition=Accepted --timeout=120s gatewayclass/higress
"${KUBECTL[@]}" -n higress-system wait --for=condition=Accepted --timeout=120s gateway/ani-higress

echo "Inference dependencies installed in context $KUBE_CONTEXT."
