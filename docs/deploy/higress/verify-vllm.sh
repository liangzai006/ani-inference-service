#!/usr/bin/env bash
set -euo pipefail

base_url="${ANI_HIGRESS_URL:-${HIGRESS_PUBLIC_BASE_URL:-}}"
if [[ -z "$base_url" ]]; then
  : "${GATEWAY_NODE_IP:?set GATEWAY_NODE_IP or HIGRESS_PUBLIC_BASE_URL to a new-cluster gateway node}"
  base_url="http://${GATEWAY_NODE_IP}:30090"
fi
api_key="${ANI_HIGRESS_API_KEY:?set ANI_HIGRESS_API_KEY after applying key-auth.yaml.tmpl}"
model="${ANI_TEST_MODEL:?set ANI_TEST_MODEL to a served model name}"
completion_url="${base_url%/}/v1/completions"

body=$(printf '{"model":"%s","prompt":"higress smoke test","max_tokens":2}' "$model")
unauthenticated_status=$(curl -sS -m 20 -o /dev/null -w '%{http_code}' \
  -H 'content-type: application/json' -d "$body" "$completion_url")
test "$unauthenticated_status" = 401
status=$(curl -sS -m 60 -o /tmp/ani-higress-completion.json -w '%{http_code}' \
  -H 'content-type: application/json' \
  -H "x-api-key: ${api_key}" \
  -d "$body" "$completion_url")
test "$status" = 200
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["model"] and d["choices"][0]["text"]' /tmp/ani-higress-completion.json

unknown='ani-unknown-model'
unknown_body=$(printf '{"model":"%s","prompt":"higress negative test","max_tokens":1}' "$unknown")
unknown_status=$(curl -sS -m 20 -o /dev/null -w '%{http_code}' \
  -H 'content-type: application/json' \
  -H "x-api-key: ${api_key}" \
  -d "$unknown_body" "$completion_url")
# The bootstrap HTTPRoute deliberately has no backend: Gateway API rejects an
# unmatched model with 500 after the plugin has had a chance to reroute it.
test "$unknown_status" = 500

printf 'Higress body-model completion passed for %s; no key returned 401; unknown model returned 500.\n' "$model"
