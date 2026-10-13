#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
public_url=${HIGRESS_PUBLIC_BASE_URL:-https://inference.example.test}

helm lint "$repo_root/charts/ani-inference"
helm lint "$repo_root/charts/ani-inference-config"
helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace ani-system \
  --set higress.publicBaseURL="$public_url" \
  >"$tmp_dir/inference.yaml"
helm template ani-inference-config "$repo_root/charts/ani-inference-config" \
  --namespace kserve \
  >"$tmp_dir/config.yaml"
test -s "$tmp_dir/inference.yaml"
test -s "$tmp_dir/config.yaml"
grep -q '^kind: Certificate$' "$tmp_dir/inference.yaml"
grep -q 'ANI_SERVER_TLS_CA_FILE' "$tmp_dir/inference.yaml"
grep -q 'ani-inference-server-tls' "$tmp_dir/inference.yaml"
grep -q 'CREATE TABLE IF NOT EXISTS public.inference_schema_migrations' "$tmp_dir/inference.yaml"
grep -q 'pg_advisory_xact_lock' "$tmp_dir/inference.yaml"
grep -q -- '--single-transaction' "$tmp_dir/inference.yaml"
grep -q 'partial legacy inference schema' "$tmp_dir/inference.yaml"
grep -q 'name: ani-inference-model-router' "$tmp_dir/inference.yaml"
grep -q 'type: PathPrefix' "$tmp_dir/inference.yaml"
if grep -Eq '^kind: (ClusterIssuer|Issuer)$' "$tmp_dir/inference.yaml"; then
  echo 'ani-inference must not create a certificate issuer' >&2
  exit 1
fi
if helm template ani-inference "$repo_root/charts/ani-inference" \
  --namespace default --set higress.publicBaseURL="$public_url" \
  >"$tmp_dir/wrong-namespace.yaml" 2>"$tmp_dir/wrong-namespace.err"; then
  echo 'ani-inference must reject namespaces other than ani-system' >&2
  exit 1
fi
grep -q 'ani-inference must be installed in namespace ani-system' "$tmp_dir/wrong-namespace.err"
echo "rendered inference charts"
