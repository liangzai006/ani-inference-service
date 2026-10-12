#!/usr/bin/env bash
set -euo pipefail

# KServe's CRDs and webhook configurations are cluster-scoped. Refuse to
# install a second controller from another namespace; the operator must first
# be migrated or stopped in the explicitly selected new cluster.
: "${KUBE_CONTEXT:?Set KUBE_CONTEXT to the new environment}"
for tool in kubectl python3; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 127; }
done

# When running in-cluster (Pod with ServiceAccount), don't use --context
if [ "$KUBE_CONTEXT" = "in-cluster" ]; then
  payload=$(kubectl get deployments.apps -A -o json)
else
  payload=$(kubectl --context "$KUBE_CONTEXT" get deployments.apps -A -o json)
fi
conflicts=$(python3 -c '
import json
import sys

data = json.load(sys.stdin)
for item in data.get("items", []):
    metadata = item.get("metadata", {})
    labels = metadata.get("labels", {})
    name = metadata.get("name", "")
    if name != "kserve-controller-manager" and labels.get("app.kubernetes.io/name") not in {
        "kserve-controller-manager", "kserve-controller"
    }:
        continue
    namespace = metadata.get("namespace", "")
    annotations = metadata.get("annotations", {})
    managed = (
        namespace == "kserve"
        and labels.get("app.kubernetes.io/managed-by") == "Helm"
        and annotations.get("meta.helm.sh/release-name") == "kserve"
        and annotations.get("meta.helm.sh/release-namespace") == "kserve"
    )
    if not managed:
        print(f"{namespace}/{name}")
' <<<"$payload")

if [[ -n "$conflicts" ]]; then
  cat >&2 <<EOF
existing KServe controller would conflict with the KServe 0.16 release:
$conflicts
stop or migrate these controllers in context $KUBE_CONTEXT, then rerun
scripts/install.sh. The installer does not delete an existing controller.
EOF
  exit 1
fi

echo "KServe controller ownership check passed for context $KUBE_CONTEXT."
