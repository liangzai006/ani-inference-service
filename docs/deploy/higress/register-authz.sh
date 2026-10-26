#!/usr/bin/env bash
set -euo pipefail

# Register the private authz endpoint in Higress service discovery without
# replacing unrelated entries or creating a public route to the adapter.
: "${KUBE_CONTEXT:?Set KUBE_CONTEXT to the intended cluster}"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
chmod 700 "$tmp_dir"
kubectl --context "$KUBE_CONTEXT" -n higress-system get mcpbridge default -o json >"$tmp_dir/bridge.json"
python3 - "$tmp_dir/bridge.json" "$tmp_dir/patch.json" <<'PY'
import json
import sys

with open(sys.argv[1]) as source:
    bridge = json.load(source)
entry = {
    "name": "ani-inference-authz",
    "type": "dns",
    "domain": "ani-inference-authz.ani-system.svc.cluster.local",
    "port": 8080,
    "protocol": "http",
}
registries = bridge.get("spec", {}).get("registries", [])
existing = [item for item in registries if item.get("name") == entry["name"]]
if existing and existing != [entry]:
    raise SystemExit("conflicting ani-inference-authz registry; inspect before replacing")
patch = [{"op": "test", "path": "/metadata/resourceVersion", "value": bridge["metadata"]["resourceVersion"]}]
if not existing:
    patch.append({"op": "add", "path": "/spec/registries", "value": registries + [entry]})
with open(sys.argv[2], "w") as target:
    json.dump(patch, target)
PY
kubectl --context "$KUBE_CONTEXT" -n higress-system patch mcpbridge default \
  --type=json --patch-file "$tmp_dir/patch.json"
