#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/kubectl" <<'STUB'
#!/usr/bin/env bash
case "${CASE:-none}" in
  none|managed)
    printf '%s\n' '{"items":[]}'
    ;;
  outside)
    printf '%s\n' '{"items":[{"metadata":{"name":"kserve-controller-manager","namespace":"kubeflow","labels":{}}}]}'
    ;;
  unmanaged)
    printf '%s\n' '{"items":[{"metadata":{"name":"kserve-controller-manager","namespace":"kserve","labels":{},"annotations":{}}}]}'
    ;;
  helm)
    printf '%s\n' '{"items":[{"metadata":{"name":"kserve-controller-manager","namespace":"kserve","labels":{"app.kubernetes.io/managed-by":"Helm"},"annotations":{"meta.helm.sh/release-name":"kserve","meta.helm.sh/release-namespace":"kserve"}}}]}'
    ;;
esac
STUB
chmod +x "$tmp/kubectl"

run_case() {
  local case_name=$1 expected=$2 output status
  set +e
  output=$(PATH="$tmp:$PATH" CASE="$case_name" KUBE_CONTEXT=new-cluster \
    "$ROOT/scripts/check-kserve-controller.sh" 2>&1)
  status=$?
  set -e
  [[ "$status" -eq "$expected" ]] || { printf '%s\n' "$output" >&2; return 1; }
}

run_case none 0
run_case helm 0
run_case outside 1
run_case unmanaged 1
echo "KServe controller conflict checks passed"
