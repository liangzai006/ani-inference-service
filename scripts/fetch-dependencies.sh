#!/usr/bin/env bash
set -euo pipefail

# Download and validate the platform artifacts used by install-dependencies.sh.
# This script only touches the local cache; it never reads or changes a
# Kubernetes context.

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
CACHE_DIR=${PLATFORM_CHART_DIR:-"$REPO_ROOT/charts/vendor"}
PIN_FILE="$CACHE_DIR/SHA256SUMS"
DOWNLOADED=false

# shellcheck disable=SC1091
source "$SCRIPT_DIR/dependency-versions.env"

: "${KSERVE_VERSION:?KSERVE_VERSION is required}"
: "${LWS_VERSION:?LWS_VERSION is required}"
: "${HIGRESS_VERSION:?HIGRESS_VERSION is required}"
: "${GATEWAY_API_VERSION:?GATEWAY_API_VERSION is required}"
: "${GIE_VERSION:?GIE_VERSION is required}"
: "${CERT_MANAGER_VERSION:?CERT_MANAGER_VERSION is required}"

HIGRESS_REPO_NAME=${HIGRESS_REPO_NAME:-higress.io}
HIGRESS_REPO_URL=${HIGRESS_REPO_URL:-https://higress.io/helm-charts}
CERT_MANAGER_REPO_NAME=${CERT_MANAGER_REPO_NAME:-jetstack}
CERT_MANAGER_REPO_URL=${CERT_MANAGER_REPO_URL:-https://charts.jetstack.io}

command -v helm >/dev/null 2>&1 || {
  echo "helm is required to download platform charts" >&2
  exit 127
}
command -v curl >/dev/null 2>&1 || {
  echo "curl is required to download platform artifacts" >&2
  exit 127
}
command -v tar >/dev/null 2>&1 || {
  echo "tar is required to validate chart archives" >&2
  exit 127
}
command -v sha256sum >/dev/null 2>&1 || {
  echo "sha256sum is required to verify platform artifacts" >&2
  exit 127
}

mkdir -p "$CACHE_DIR"

expected_hash() {
  [[ -f "$PIN_FILE" ]] || return 0
  awk -v name="$1" '$2 == name { print $1; exit }' "$PIN_FILE"
}

validate_hash() {
  local file=$1
  local name=$2
  local allow_unpinned=${3:-false}
  local expected actual
  expected=$(expected_hash "$name")
  if [[ -z "$expected" ]]; then
    [[ "$allow_unpinned" == true ]]
    return
  fi
  actual=$(sha256sum "$file")
  [[ "${actual%% *}" == "$expected" ]]
}

chart_field() {
  local archive=$1
  local field=$2
  helm show chart "$archive" 2>/dev/null |
    awk -F': ' -v field="$field" '$1 == field { print $2; exit }'
}

validate_chart() {
  local archive=$1
  local expected_name=$2
  local expected_version=$3
  [[ -s "$archive" ]] || return 1
  tar -tzf "$archive" >/dev/null 2>&1 || return 1
  [[ "$(chart_field "$archive" name)" == "$expected_name" ]] || return 1
  [[ "$(chart_field "$archive" version)" == "$expected_version" ]] || return 1
}

remove_invalid_cache() {
  local archive=$1
  if [[ -e "$archive" ]]; then
    echo "invalid cached artifact; replacing: $archive" >&2
    rm -f -- "$archive"
  fi
}

download_release_chart() {
  download_release_asset kserve/kserve "$@"
}

download_release_asset() {
  local project=$1
  local artifact=$2
  local release_tag=$3
  local release_asset=$4
  local expected_name=$5
  local archive="$CACHE_DIR/${artifact}-${release_tag}.tgz"
  local temporary="${archive}.part.$$"

  if validate_chart "$archive" "$expected_name" "$release_tag" && \
    validate_hash "$archive" "${artifact}-${release_tag}.tgz"; then
    echo "using cached chart $archive"
    return 0
  fi
  remove_invalid_cache "$archive"
  rm -f -- "$temporary"
  if ! curl --fail --location --retry 3 --silent --show-error \
    "https://github.com/${project}/releases/download/${release_tag}/${release_asset}" \
    --output "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  if ! validate_chart "$temporary" "$expected_name" "$release_tag" || \
    ! validate_hash "$temporary" "${artifact}-${release_tag}.tgz" true; then
    echo "downloaded chart metadata or SHA256 is invalid: $release_asset" >&2
    rm -f -- "$temporary"
    return 1
  fi
  mv -f -- "$temporary" "$archive"
  DOWNLOADED=true
  echo "downloaded and validated $archive"
}

download_helm_chart() {
  local artifact=$1
  local chart_ref=$2
  local chart_version=$3
  local expected_name=$4
  local repo_name=$5
  local repo_url=$6
  local archive="$CACHE_DIR/${artifact}-${chart_version}.tgz"
  local temporary_directory temporary_archive

  if validate_chart "$archive" "$expected_name" "$chart_version" && \
    validate_hash "$archive" "${artifact}-${chart_version}.tgz"; then
    echo "using cached chart $archive"
    return 0
  fi
  remove_invalid_cache "$archive"
  helm repo add --force-update "$repo_name" "$repo_url" >/dev/null
  helm repo update "$repo_name" >/dev/null
  temporary_directory=$(mktemp -d "$CACHE_DIR/.download-${artifact}.XXXXXX")
  if ! helm pull "$chart_ref" --version "$chart_version" --destination "$temporary_directory"; then
    rm -rf -- "$temporary_directory"
    return 1
  fi
  temporary_archive=$(find "$temporary_directory" -maxdepth 1 -type f -name '*.tgz' -print -quit)
  if [[ -z "$temporary_archive" ]] || ! validate_chart "$temporary_archive" "$expected_name" "$chart_version" || \
    ! validate_hash "$temporary_archive" "${artifact}-${chart_version}.tgz" true; then
    echo "downloaded chart metadata or SHA256 is invalid: $chart_ref@$chart_version" >&2
    rm -rf -- "$temporary_directory"
    return 1
  fi
  mv -f -- "$temporary_archive" "$archive"
  rm -rf -- "$temporary_directory"
  DOWNLOADED=true
  echo "downloaded and validated $archive"
}

download_manifest() {
  local artifact=$1
  local url=$2
  local marker=$3
  local destination="$CACHE_DIR/${artifact}.yaml"
  local temporary="${destination}.part.$$"

  if [[ -s "$destination" ]] && grep -Fq -- "$marker" "$destination" && \
    validate_hash "$destination" "${artifact}.yaml"; then
    echo "using cached manifest $destination"
    return 0
  fi
  remove_invalid_cache "$destination"
  rm -f -- "$temporary"
  if ! curl --fail --location --retry 3 --silent --show-error "$url" \
    --output "$temporary"; then
    rm -f -- "$temporary"
    return 1
  fi
  if [[ ! -s "$temporary" ]] || ! grep -Fq -- "$marker" "$temporary" || \
    ! validate_hash "$temporary" "${artifact}.yaml" true; then
    echo "downloaded manifest failed validation: $url" >&2
    rm -f -- "$temporary"
    return 1
  fi
  mv -f -- "$temporary" "$destination"
  DOWNLOADED=true
  echo "downloaded and validated $destination"
}

# KServe's v0.16 release publishes reproducible chart archives. Using release
# assets avoids changing the chart package selected by an OCI tag and also
# makes the LLM chart available when GHCR rate-limits anonymous pulls.
download_release_chart kserve-crd "$KSERVE_VERSION" \
  "helm-chart-kserve-crd-${KSERVE_VERSION}.tgz" kserve-crd
download_release_chart kserve "$KSERVE_VERSION" \
  "helm-chart-kserve-${KSERVE_VERSION}.tgz" kserve
download_release_chart llmisvc-crd "$KSERVE_VERSION" \
  "helm-chart-llmisvc-crd-${KSERVE_VERSION}.tgz" llmisvc-crd
download_release_chart llmisvc "$KSERVE_VERSION" \
  "helm-chart-llmisvc-resources-${KSERVE_VERSION}.tgz" llmisvc-resources

download_release_asset kubernetes-sigs/lws lws "$LWS_VERSION" \
  "lws-chart-${LWS_VERSION}.tgz" lws
download_helm_chart cert-manager "$CERT_MANAGER_REPO_NAME/cert-manager" "$CERT_MANAGER_VERSION" cert-manager \
  "$CERT_MANAGER_REPO_NAME" "$CERT_MANAGER_REPO_URL"
download_helm_chart higress "$HIGRESS_REPO_NAME/higress" "$HIGRESS_VERSION" higress \
  "$HIGRESS_REPO_NAME" "$HIGRESS_REPO_URL"

download_manifest \
  "gateway-api-${GATEWAY_API_VERSION}-standard-install" \
  "https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml" \
  "gateways.gateway.networking.k8s.io"
download_manifest \
  "gateway-api-inference-extension-${GIE_VERSION}-manifests" \
  "https://github.com/kubernetes-sigs/gateway-api-inference-extension/releases/download/${GIE_VERSION}/manifests.yaml" \
  "inferencepools.inference.networking.x-k8s.io"

if [[ "$DOWNLOADED" == true ]]; then
  (
    cd -- "$CACHE_DIR"
    sha256sum \
      "gateway-api-inference-extension-${GIE_VERSION}-manifests.yaml" \
      "cert-manager-${CERT_MANAGER_VERSION}.tgz" \
      "gateway-api-${GATEWAY_API_VERSION}-standard-install.yaml" \
      "higress-${HIGRESS_VERSION}.tgz" \
      "kserve-crd-${KSERVE_VERSION}.tgz" \
      "kserve-${KSERVE_VERSION}.tgz" \
      "llmisvc-crd-${KSERVE_VERSION}.tgz" \
      "llmisvc-${KSERVE_VERSION}.tgz" \
      "lws-${LWS_VERSION}.tgz" >"SHA256SUMS.part.$$"
    mv -f -- "SHA256SUMS.part.$$" SHA256SUMS
  )
fi

cat <<EOF
Platform dependency artifacts are ready in:
  $CACHE_DIR

Charts:
  kserve-crd@$KSERVE_VERSION
  kserve@$KSERVE_VERSION
  llmisvc-crd@$KSERVE_VERSION
  llmisvc@$KSERVE_VERSION (chart metadata: llmisvc-resources)
  lws@$LWS_VERSION
  cert-manager@$CERT_MANAGER_VERSION
  higress@$HIGRESS_VERSION
CRD manifests:
  Gateway API standard $GATEWAY_API_VERSION
  Gateway API Inference Extension $GIE_VERSION
EOF
