#!/usr/bin/env bash
set -euo pipefail
# Deterministic API validation/generation; dependencies are temporary inputs only.
# Accelerator Proto and Go packages must come from the same released module.
# Use this script for lint/generate so the locked Accelerator module is mounted
# as a second input without copying its proto into this repository.
cd "$(dirname "$0")/.."
export GOWORK=off
BUF=${BUF:-buf}
action=${1:-generate}
case "$action" in
  lint|generate|check) ;;
  *) echo "usage: $0 [lint|generate|check]" >&2; exit 2 ;;
esac
test "$("$BUF" --version)" = 1.60.0
go version -m "$(command -v "$BUF")" | grep -Eq 'mod[[:space:]]+github.com/bufbuild/buf[[:space:]]+v1.60.0'
acc=github.com/zhangzhe-ctrl/ani-accelerator-service
version=$(GOWORK=off go -C api list -m -f '{{.Version}}' "$acc")
test "$version" = v0.0.0-20261018033121-4f084c9f5f25
GOWORK=off go -C api mod download "$acc@$version"
acc_dir=$(GOWORK=off go -C api list -m -f '{{.Dir}}' "$acc")
work=$(mktemp -d "${TMPDIR:-/tmp}/inference-api.XXXXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/upstream"
cp -R api "$work/api"
mkdir -p "$work/internal"
cp -R internal/conf "$work/internal/conf"
cp go.mod go.sum buf.gen.yaml buf.config.gen.yaml "$work/"
cp -R "$acc_dir/api/protos" "$work/upstream/accelerator"
chmod -R u+w "$work/upstream"
printf '%s\n' \
  'version: v2' \
  'modules:' \
  '  - path: api' \
  '  - path: internal' \
  '    lint:' \
  '      use: [MINIMAL]' \
  '      except: [PACKAGE_DIRECTORY_MATCH]' \
  '  - path: upstream/accelerator' \
  'lint:' \
  '  use:' \
  '    - MINIMAL' > "$work/buf.yaml"
before=$(find api internal/conf -type f -print0 | sort -z | xargs -0 -r sha256sum)
if [ "$action" = lint ]; then
  (cd "$work" && "$BUF" lint)
  exit 0
fi
(cd "$work" && "$BUF" lint && "$BUF" generate --template buf.gen.yaml && "$BUF" generate --template buf.config.gen.yaml)
test "$before" = "$(find api internal/conf -type f -print0 | sort -z | xargs -0 -r sha256sum)" || {
  echo 'API input/output changed during generation; no output copied' >&2
  exit 1
}
while IFS= read -r -d '' file; do
  relative=${file#"$work/"}
  if [ "$action" = check ]; then
    diff -u "$relative" "$file"
  else
    cp "$file" "$relative"
  fi
done < <(find "$work/api" "$work/internal/conf" -type f -name '*pb.go' -print0)
