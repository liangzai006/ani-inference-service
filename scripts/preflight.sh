#!/usr/bin/env bash
set -euo pipefail

# Required:
#   KUBE_CONTEXT                         kubectl context; never inferred from current-context.
# Optional overrides (defaults match charts/ and docs/):
#   KUBE_REQUEST_TIMEOUT                kubectl request timeout (default: 20s)
#   ANI_NAMESPACE                       shared service namespace (default: ani-system)
#   ANI_DATABASE_SECRET_NAME            database Secret (default: inference-database)
#   ANI_DATABASE_SECRET_KEY             required key in that Secret (default: dsn)
#   ANI_MODEL_SERVICE_NAME              Model gRPC Service (default: ani-model-grpc)
#   ANI_MODEL_ISSUER_NAME               Model client Certificate issuer (default: ani-internal)
#   ANI_HIGRESS_GATEWAY_NAMESPACE       Gateway namespace (default: higress-system)
#   ANI_HIGRESS_GATEWAY_NAME            Gateway name (default: ani-higress)
#   ANI_HIGRESS_GATEWAY_CLASS           GatewayClass name (default: higress)
#   ANI_HIGRESS_RATE_LIMIT_NAMESPACE    WasmPlugin namespace (default: higress-system)
#   ANI_HIGRESS_RATE_LIMIT_PLUGIN       WasmPlugin name (default: ani-request-rate-limit)
#   ANI_KSERVE_LLM_CONFIG_NAMESPACE     optional namespace for the KServe LLM pipeline config
#   ANI_KSERVE_LLM_CONFIG_NAME          optional config name (default: kserve-config-llm-worker-pipeline-parallel)
# Optional controller availability checks. Set namespace and name together; no
# controller Deployment is assumed because platform installations use different
# namespaces (for example KServe may run in kubeflow or kserve):
#   ANI_KSERVE_CONTROLLER_NAMESPACE / ANI_KSERVE_CONTROLLER_NAME
#   ANI_LWS_CONTROLLER_NAMESPACE    / ANI_LWS_CONTROLLER_NAME
#   ANI_HIGRESS_CONTROLLER_NAMESPACE/ ANI_HIGRESS_CONTROLLER_NAME

if [[ -z "${KUBE_CONTEXT:-}" ]]; then
  echo "KUBE_CONTEXT is required; refusing to use the current kubectl context" >&2
  exit 2
fi

request_timeout="${KUBE_REQUEST_TIMEOUT:-20s}"
namespace="${ANI_NAMESPACE:-ani-system}"
database_secret="${ANI_DATABASE_SECRET_NAME:-inference-database}"
database_secret_key="${ANI_DATABASE_SECRET_KEY:-dsn}"
model_service="${ANI_MODEL_SERVICE_NAME:-ani-model-grpc}"
model_issuer="${ANI_MODEL_ISSUER_NAME:-ani-internal}"
gateway_namespace="${ANI_HIGRESS_GATEWAY_NAMESPACE:-higress-system}"
gateway_name="${ANI_HIGRESS_GATEWAY_NAME:-ani-higress}"
gateway_class="${ANI_HIGRESS_GATEWAY_CLASS:-higress}"
rate_limit_namespace="${ANI_HIGRESS_RATE_LIMIT_NAMESPACE:-higress-system}"
rate_limit_plugin="${ANI_HIGRESS_RATE_LIMIT_PLUGIN:-ani-request-rate-limit}"
llm_config_namespace="${ANI_KSERVE_LLM_CONFIG_NAMESPACE:-}"
llm_config_name="${ANI_KSERVE_LLM_CONFIG_NAME:-kserve-config-llm-worker-pipeline-parallel}"

kubectl_cmd=(kubectl --context "$KUBE_CONTEXT" --request-timeout "$request_timeout")

fail() {
  echo "preflight failed: $*" >&2
  exit 1
}

check_get() {
  local description=$1
  shift
  printf 'checking %s\n' "$description"
  if ! "${kubectl_cmd[@]}" get "$@" -o name >/dev/null 2>/dev/null; then
    fail "$description is unavailable (check context, permissions, and platform state)"
  fi
}

# Conditions are read from status only; this never mutates the object. The
# status output contains no Secret data and is discarded after the comparison.
check_condition() {
  local description=$1 kind=$2 name=$3 namespace_arg=$4 expected=$5
  local conditions
  printf 'checking %s\n' "$description"
  if [[ -n "$namespace_arg" ]]; then
    if ! conditions=$("${kubectl_cmd[@]}" get "$kind" "$name" -n "$namespace_arg" \
      -o 'jsonpath={range .status.conditions[*]}{.type}={.status}{","}{end}' 2>/dev/null); then
      fail "$description cannot be read (check context, permissions, and platform state)"
    fi
  else
    if ! conditions=$("${kubectl_cmd[@]}" get "$kind" "$name" \
      -o 'jsonpath={range .status.conditions[*]}{.type}={.status}{","}{end}' 2>/dev/null); then
      fail "$description cannot be read (check context, permissions, and platform state)"
    fi
  fi
  if [[ ",$conditions" != *",$expected=True,"* ]]; then
    fail "$description is not ready (expected condition ${expected}=True)"
  fi
}

check_crd() {
  local crd=$1 version=$2 fields deletion conditions versions
  printf 'checking CRD %s (served %s)\n' "$crd" "$version"
  if ! fields=$("${kubectl_cmd[@]}" get crd "$crd" \
    -o 'jsonpath={.metadata.deletionTimestamp}{"|"}{range .status.conditions[*]}{.type}={.status}{","}{end}{"|"}{range .spec.versions[*]}{.name}={.served}{","}{end}' 2>/dev/null); then
    fail "CRD $crd cannot be read (check context, permissions, and platform state)"
  fi
  IFS='|' read -r deletion conditions versions <<< "$fields"
  [[ -z "$deletion" && ",$conditions" == *",Established=True,"* ]] || fail "CRD $crd is not Established or is terminating"
  [[ ",$versions" == *",$version=true,"* ]] || fail "CRD $crd does not serve $version"
}

check_api_group() {
  local group=$1 resources resource
  shift
  printf 'checking API discovery %s\n' "$group"
  if ! resources=$("${kubectl_cmd[@]}" api-resources --cached=false --api-group "$group" -o name 2>/dev/null); then
    fail "API discovery for $group failed"
  fi
  for resource in "$@"; do
    [[ $'\n'"$resources"$'\n' == *$'\n'"$resource.$group"$'\n'* ]] || fail "API discovery is missing $resource.$group"
  done
}

check_optional_controller() {
  local label=$1 controller_namespace=$2 controller_name=$3
  if [[ -z "$controller_namespace" && -z "$controller_name" ]]; then
    return 0
  fi
  [[ -n "$controller_namespace" && -n "$controller_name" ]] || \
    fail "$label controller namespace and name must be set together"
  check_get "$label controller Deployment $controller_namespace/$controller_name" \
    deployment "$controller_name" -n "$controller_namespace"
  check_condition "$label controller Deployment $controller_namespace/$controller_name Available" \
    deployment "$controller_name" "$controller_namespace" Available
}

# CRDs are checked for Established=True, rather than merely existing. These
# groups are used by the controller, runtime adapters, and publication adapter.
check_crd inferenceservices.ani.kubercloud.com v1
check_crd inferenceservices.serving.kserve.io v1beta1
check_crd llminferenceservices.serving.kserve.io v1alpha1
check_crd leaderworkersets.leaderworkerset.x-k8s.io v1
check_crd gatewayclasses.gateway.networking.k8s.io v1
check_crd gateways.gateway.networking.k8s.io v1
check_crd httproutes.gateway.networking.k8s.io v1
check_crd wasmplugins.extensions.higress.io v1alpha1

check_api_group ani.kubercloud.com inferenceservices
check_api_group serving.kserve.io inferenceservices llminferenceservices
check_api_group leaderworkerset.x-k8s.io leaderworkersets
check_api_group gateway.networking.k8s.io gatewayclasses gateways httproutes
check_api_group extensions.higress.io wasmplugins

if [[ -n "$llm_config_namespace" || "${ANI_KSERVE_LLM_CONFIG_NAME:-}" != "" ]]; then
  [[ -n "$llm_config_namespace" ]] || fail "ANI_KSERVE_LLM_CONFIG_NAMESPACE is required when ANI_KSERVE_LLM_CONFIG_NAME is set"
  check_crd llminferenceserviceconfigs.serving.kserve.io v1alpha1
  check_api_group serving.kserve.io llminferenceserviceconfigs
  check_get "KServe LLM pipeline config $llm_config_namespace/$llm_config_name" \
    llminferenceserviceconfigs "$llm_config_name" -n "$llm_config_namespace"
fi

check_get "namespace $namespace" namespace "$namespace"
namespace_environment=''
if ! namespace_environment=$("${kubectl_cmd[@]}" get namespace "$namespace" \
  -o 'jsonpath={.metadata.labels.ani\.liangzai006\.io/environment}' 2>/dev/null); then
  fail "namespace $namespace labels cannot be read"
fi
[[ "$namespace_environment" == "new-cluster" ]] || \
  fail "namespace $namespace must have label ani.liangzai006.io/environment=new-cluster for the Higress route selector"
unset namespace_environment
# Use -o name for Secret checks; never print Secret data or YAML.
check_get "database Secret $namespace/$database_secret" secret "$database_secret" -n "$namespace"
printf 'checking database Secret key %s/%s[%s]\n' "$namespace" "$database_secret" "$database_secret_key"
secret_key_value=''
if ! secret_key_value=$("${kubectl_cmd[@]}" get secret "$database_secret" -n "$namespace" \
  -o "jsonpath={.data.${database_secret_key}}" 2>/dev/null); then
  fail "database Secret $namespace/$database_secret key cannot be read"
fi
[[ -n "$secret_key_value" ]] || fail "database Secret $namespace/$database_secret is missing key $database_secret_key"
unset secret_key_value
check_get "Model gRPC Service $namespace/$model_service" service "$model_service" -n "$namespace"
check_get "Model client ClusterIssuer $model_issuer" clusterissuer "$model_issuer"
check_get "Inference client Certificate $namespace/inference-model-mtls" \
  certificate inference-model-mtls -n "$namespace"
check_condition "Inference client Certificate $namespace/inference-model-mtls Ready" \
  certificate inference-model-mtls "$namespace" Ready
for certificate_key in ca.crt tls.crt tls.key; do
  printf 'checking Inference client Certificate Secret %s/inference-model-mtls[%s]\n' "$namespace" "$certificate_key"
  certificate_key_value=''
  if ! certificate_key_value=$("${kubectl_cmd[@]}" get secret inference-model-mtls -n "$namespace" \
    -o "jsonpath={.data.${certificate_key}}" 2>/dev/null); then
    fail "Inference client Certificate Secret $namespace/inference-model-mtls cannot be read"
  fi
  [[ -n "$certificate_key_value" ]] || \
    fail "Inference client Certificate Secret $namespace/inference-model-mtls is missing key $certificate_key"
  unset certificate_key_value
done

check_get "GatewayClass $gateway_class" gatewayclass "$gateway_class"
check_condition "GatewayClass $gateway_class Accepted" gatewayclass "$gateway_class" "" Accepted
check_get "Gateway $gateway_namespace/$gateway_name" gateway "$gateway_name" -n "$gateway_namespace"
check_condition "Gateway $gateway_namespace/$gateway_name Accepted" gateway "$gateway_name" "$gateway_namespace" Accepted
check_get "Higress WasmPlugin $rate_limit_namespace/$rate_limit_plugin" \
  wasmplugin "$rate_limit_plugin" -n "$rate_limit_namespace"

plugin_header=''
if ! plugin_header=$("${kubectl_cmd[@]}" get wasmplugin "$rate_limit_plugin" -n "$rate_limit_namespace" \
  -o 'jsonpath={.spec.defaultConfig.limit_by_header}' 2>/dev/null); then
  fail "Higress WasmPlugin $rate_limit_namespace/$rate_limit_plugin configuration cannot be read"
fi
[[ "$plugin_header" == "x-higress-llm-model" ]] || \
  fail "Higress WasmPlugin $rate_limit_namespace/$rate_limit_plugin must set spec.defaultConfig.limit_by_header=x-higress-llm-model"

check_optional_controller "KServe" \
  "${ANI_KSERVE_CONTROLLER_NAMESPACE:-}" "${ANI_KSERVE_CONTROLLER_NAME:-}"
check_optional_controller "LWS" \
  "${ANI_LWS_CONTROLLER_NAMESPACE:-}" "${ANI_LWS_CONTROLLER_NAME:-}"
check_optional_controller "Higress" \
  "${ANI_HIGRESS_CONTROLLER_NAMESPACE:-}" "${ANI_HIGRESS_CONTROLLER_NAME:-}"

echo "Inference read-only preflight checks passed for namespace $namespace"
echo "Controller Deployments are checked only when configured; database connectivity, Model endpoints, storage, and inference traffic still require separate validation."
