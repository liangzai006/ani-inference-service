#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
public_url=${HIGRESS_PUBLIC_BASE_URL:-https://inference.example.test}
console_url=${HIGRESS_CONSOLE_URL:-http://higress-console.higress-system.svc.cluster.local:8080}
console_secret=${HIGRESS_CONSOLE_CREDENTIALS_SECRET:-ani-higress-console-credentials}
common_values=(
  --set higress.publicBaseURL="$public_url"
  --set higress.consoleURL="$console_url"
  --set higress.consoleCredentialsSecret.name="$console_secret"
)

helm lint "$repo_root/charts/ani-inference"
helm lint "$repo_root/charts/ani-inference-config"
helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace ani-system \
  "${common_values[@]}" \
  >"$tmp_dir/inference.yaml"
helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace ani-system \
  "${common_values[@]}" \
  --show-only templates/higress-rbac.yaml \
  >"$tmp_dir/higress-rbac.yaml"
helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace ani-system \
  "${common_values[@]}" \
  --set higress.rateLimitNamespace=custom-higress-system \
  --set higress.rateLimitPlugin=custom-rate-limit \
  --set higress.rbac.namespace=custom-higress-system \
  --set serviceAccount.name=custom-control-plane \
  --show-only templates/higress-rbac.yaml \
  >"$tmp_dir/higress-rbac-custom.yaml"
helm template ani-inference-config "$repo_root/charts/ani-inference-config" \
  --namespace kserve \
  >"$tmp_dir/config.yaml"
test -s "$tmp_dir/inference.yaml"
test -s "$tmp_dir/config.yaml"
test -s "$tmp_dir/higress-rbac.yaml"
test -s "$tmp_dir/higress-rbac-custom.yaml"
grep -q '^kind: Role$' "$tmp_dir/higress-rbac.yaml"
grep -q '^kind: RoleBinding$' "$tmp_dir/higress-rbac.yaml"
grep -q '^  namespace: higress-system$' "$tmp_dir/higress-rbac.yaml"
grep -q '^    resourceNames: \["ani-request-rate-limit"\]$' "$tmp_dir/higress-rbac.yaml"
grep -q '^    verbs: \["get", "patch"\]$' "$tmp_dir/higress-rbac.yaml"
grep -q '^    name: ani-inference$' "$tmp_dir/higress-rbac.yaml"
grep -q '^    namespace: ani-system$' "$tmp_dir/higress-rbac.yaml"
test "$(grep -c '^  namespace: higress-system$' "$tmp_dir/higress-rbac.yaml")" -eq 2
grep -q '^  namespace: custom-higress-system$' "$tmp_dir/higress-rbac-custom.yaml"
grep -q '^    resourceNames: \["custom-rate-limit"\]$' "$tmp_dir/higress-rbac-custom.yaml"
grep -q '^    name: custom-control-plane$' "$tmp_dir/higress-rbac-custom.yaml"
test "$(grep -c '^  namespace: custom-higress-system$' "$tmp_dir/higress-rbac-custom.yaml")" -eq 2
grep -q '^kind: Certificate$' "$tmp_dir/inference.yaml"
grep -q 'ANI_SERVER_TLS_CA_FILE' "$tmp_dir/inference.yaml"
grep -q 'ani-inference-server-tls' "$tmp_dir/inference.yaml"
grep -q 'CREATE TABLE IF NOT EXISTS public.inference_schema_migrations' "$tmp_dir/inference.yaml"
grep -q 'pg_advisory_xact_lock' "$tmp_dir/inference.yaml"
grep -q -- '--single-transaction' "$tmp_dir/inference.yaml"
grep -q 'partial legacy inference schema' "$tmp_dir/inference.yaml"
grep -q 'name: ani-inference-model-router' "$tmp_dir/inference.yaml"
grep -q 'name: ANI_HIGRESS_ROUTE_PREFIX' "$tmp_dir/inference.yaml"
if grep -Eq '^kind: (ClusterIssuer|Issuer)$' "$tmp_dir/inference.yaml"; then
  echo 'ani-inference must not create a certificate issuer' >&2
  exit 1
fi
if helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace default "${common_values[@]}" \
  >"$tmp_dir/wrong-namespace.yaml" 2>"$tmp_dir/wrong-namespace.err"; then
  echo 'ani-inference must reject namespaces other than ani-system' >&2
  exit 1
fi
grep -q 'ani-inference must be installed in namespace ani-system' "$tmp_dir/wrong-namespace.err"
echo "rendered inference charts"
