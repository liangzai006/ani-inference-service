#!/usr/bin/env bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/bin"

cat > "$tmp_dir/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail

: "${KUBECTL_LOG:?}"
printf '%s\n' "$*" >> "$KUBECTL_LOG"

if [[ "${STUB_MODE:-}" == forbidden ]]; then
  echo 'Error from server (Forbidden): access denied' >&2
  exit 1
fi
if [[ "${STUB_MODE:-}" == unreachable ]]; then
  echo 'Unable to connect to the server: dial tcp timeout' >&2
  exit 1
fi

context=''
timeout=''
while (($#)); do
  case "$1" in
    --context) context=${2:-}; shift 2 ;;
    --request-timeout) timeout=${2:-}; shift 2 ;;
    --request-timeout=*) timeout=${1#*=}; shift ;;
    *) break ;;
  esac
done
if [[ -n "${EXPECTED_CONTEXT:-}" && "$context" != "$EXPECTED_CONTEXT" ]]; then
  echo "unexpected context: $context" >&2
  exit 1
fi
if [[ -n "${EXPECTED_TIMEOUT:-}" && "$timeout" != "$EXPECTED_TIMEOUT" ]]; then
  echo "unexpected timeout: $timeout" >&2
  exit 1
fi

command=${1:-}
shift || true
if [[ "$command" == api-resources ]]; then
  if [[ "${STUB_MODE:-}" == empty-discovery ]]; then exit 0; fi
  printf '%s\n' \
    inferenceservices.ani.kubercloud.com \
    inferenceservices.serving.kserve.io \
    llminferenceservices.serving.kserve.io \
    llminferenceserviceconfigs.serving.kserve.io \
    leaderworkersets.leaderworkerset.x-k8s.io \
    gatewayclasses.gateway.networking.k8s.io \
    gateways.gateway.networking.k8s.io \
    httproutes.gateway.networking.k8s.io \
    wasmplugins.extensions.higress.io
  exit 0
fi
if [[ "$command" != get ]]; then
  echo "unexpected kubectl command: $command" >&2
  exit 1
fi

resource=''
name=''
namespace=''
output=''
while (($#)); do
  case "$1" in
    -n|--namespace) namespace=${2:-}; shift 2 ;;
    -o|--output) output=${2:-}; shift 2 ;;
    --namespace=*) namespace=${1#*=}; shift ;;
    --output=*) output=${1#*=}; shift ;;
    *)
      if [[ -z "$resource" ]]; then resource=$1
      elif [[ -z "$name" ]]; then name=$1
      fi
      shift ;;
  esac
done
key="$resource/$name"
if [[ " ${STUB_MISSING:-} " == *" $key "* ]]; then
  echo "Error from server (NotFound): $key" >&2
  exit 1
fi
if [[ "${STUB_CONTROLLER_UNAVAILABLE:-}" == "$key" ]]; then
  if [[ "$output" == jsonpath=* ]]; then
    printf 'Available=False,'
    exit 0
  fi
  echo "$key"
  exit 0
fi
if [[ "$output" == go-template=* ]]; then
  if [[ "${STUB_MODE:-}" != missing-secret-key ]]; then printf 'present'; fi
elif [[ "$output" == jsonpath=* ]]; then
  case "$resource" in
    crd)
      if [[ "${STUB_MODE:-}" == unestablished-crd ]]; then
        printf '|Established=False,|v1=true,v1beta1=true,v1alpha1=true,'
      elif [[ "${STUB_MODE:-}" == unserved-crd ]]; then
        printf '|Established=True,|v2=true,'
      else
        printf '|Established=True,|v1=true,v1beta1=true,v1alpha1=true,'
      fi ;;
    deployment)
      printf 'Available=True,' ;;
    gatewayclass)
      printf 'Accepted=True,' ;;
    gateway)
      printf 'Accepted=True,Programmed=True,' ;;
    wasmplugin)
      if [[ "${STUB_MODE:-}" == wrong-plugin-header ]]; then printf 'other'; else printf 'x-higress-llm-model'; fi ;;
    certificate)
      printf 'Ready=True,' ;;
    secret)
      if [[ "${STUB_MODE:-}" == secret-has-credential ]]; then printf 'super-secret-dsn'; elif [[ "${STUB_MODE:-}" != missing-secret-key ]]; then printf 'present'; fi ;;
    namespace)
      if [[ "${STUB_MODE:-}" == wrong-namespace-label ]]; then printf 'old-cluster'; else printf 'new-cluster'; fi ;;
    *)
      printf '' ;;
  esac
else
  printf '%s\n' "$key"
fi
STUB
chmod +x "$tmp_dir/bin/kubectl"

run_case() {
  local name=$1 expected=$2
  shift 2
  local output status
  set +e
  output=$(env PATH="$tmp_dir/bin:$PATH" KUBECTL_LOG="$tmp_dir/kubectl.log" EXPECTED_CONTEXT=unit-test EXPECTED_TIMEOUT=7s KUBE_CONTEXT=unit-test KUBE_REQUEST_TIMEOUT=7s "$@" 2>&1)
  status=$?
  set -e
  if [[ "$expected" == pass && $status -ne 0 ]]; then
    printf '%s\n' "$output" >&2
    echo "$name: expected pass (status $status)" >&2
    return 1
  fi
  if [[ "$expected" == fail && $status -eq 0 ]]; then
    printf '%s\n' "$output" >&2
    echo "$name: expected failure" >&2
    return 1
  fi
  printf '%s\n' "$output"
}

preflight="$repo_root/scripts/preflight.sh"

: > "$tmp_dir/kubectl.log"
run_case pass pass "$preflight" >/dev/null
grep -F -- '--context unit-test' "$tmp_dir/kubectl.log" >/dev/null
grep -F -- '--request-timeout 7s' "$tmp_dir/kubectl.log" >/dev/null
if grep -Eq '(^| )(apply|create|delete|patch|replace|edit)( |$)' "$tmp_dir/kubectl.log"; then
  echo 'pass: preflight must be read-only' >&2
  exit 1
fi

: > "$tmp_dir/kubectl.log"
run_case missing-crd fail env STUB_MISSING='crd/inferenceservices.ani.kubercloud.com' "$preflight" >/dev/null

: > "$tmp_dir/kubectl.log"
run_case missing-secret fail env STUB_MISSING='secret/inference-database' "$preflight" >/dev/null

: > "$tmp_dir/kubectl.log"
run_case forbidden fail env STUB_MODE=forbidden "$preflight" >/dev/null

: > "$tmp_dir/kubectl.log"
run_case unreachable fail env STUB_MODE=unreachable "$preflight" >/dev/null

set +e
context_output=$(env PATH="$tmp_dir/bin:$PATH" KUBECTL_LOG="$tmp_dir/kubectl.log" EXPECTED_CONTEXT=unit-test EXPECTED_TIMEOUT=7s KUBE_REQUEST_TIMEOUT=7s env -u KUBE_CONTEXT "$preflight" 2>&1)
context_status=$?
set -e
if [[ $context_status -eq 0 || "$context_output" != *KUBE_CONTEXT* ]]; then
  echo 'context-required: expected KUBE_CONTEXT failure' >&2
  exit 1
fi

: > "$tmp_dir/kubectl.log"
run_case controller-unavailable fail env ANI_LWS_CONTROLLER_NAMESPACE=lws-system ANI_LWS_CONTROLLER_NAME=lws-controller-manager STUB_CONTROLLER_UNAVAILABLE='deployment/lws-controller-manager' "$preflight" >/dev/null

for mode in unestablished-crd unserved-crd empty-discovery wrong-plugin-header missing-secret-key wrong-namespace-label; do
  run_case "$mode" fail env STUB_MODE="$mode" "$preflight" >/dev/null
done
secret_output=$(run_case secret-redaction pass env STUB_MODE=secret-has-credential "$preflight")
if [[ "$secret_output" == *super-secret-dsn* ]]; then
  echo 'secret-redaction: preflight must not print Secret data' >&2
  exit 1
fi
run_case controller-pair-required fail env ANI_LWS_CONTROLLER_NAMESPACE=lws-system "$preflight" >/dev/null
run_case llm-config pass env ANI_KSERVE_LLM_CONFIG_NAMESPACE=kubeflow "$preflight" >/dev/null
run_case llm-config-name-without-namespace fail env ANI_KSERVE_LLM_CONFIG_NAME=custom "$preflight" >/dev/null
run_case controllers-available pass env \
  ANI_KSERVE_CONTROLLER_NAMESPACE=custom-kserve ANI_KSERVE_CONTROLLER_NAME=custom-kserve \
  ANI_LWS_CONTROLLER_NAMESPACE=custom-lws ANI_LWS_CONTROLLER_NAME=custom-lws \
  ANI_HIGRESS_CONTROLLER_NAMESPACE=custom-higress ANI_HIGRESS_CONTROLLER_NAME=custom-higress \
  "$preflight" >/dev/null

printf 'preflight stub tests passed\n' 
