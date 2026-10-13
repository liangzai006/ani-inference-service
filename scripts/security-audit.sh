#!/usr/bin/env bash
set -euo pipefail

# Keep security scanners local to this repository. They are build tools, not
# runtime dependencies.
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tools_dir=${SECURITY_TOOLS_DIR:-"$repo_root/.tools/bin"}
govulncheck="$tools_dir/govulncheck"
gitleaks="$tools_dir/gitleaks"

mkdir -p "$tools_dir"
GOWORK=off GOBIN="$tools_dir" go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
GOWORK=off GOBIN="$tools_dir" go install github.com/zricethezav/gitleaks/v8@v8.30.1

"$govulncheck" -version | grep --fixed-strings --line-regexp 'Scanner: govulncheck@v1.7.0'
test "$(go version -m "$govulncheck" | awk '$1 == "mod" {print $2 "@" $3; exit}')" = 'golang.org/x/vuln@v1.7.0'
test "$(go version -m "$gitleaks" | awk '$1 == "mod" {print $2 "@" $3; exit}')" = 'github.com/zricethezav/gitleaks/v8@v8.30.1'

case "${1:-audit}" in
  vuln)
    (cd "$repo_root" && "$govulncheck" -show version,verbose ./...)
    ;;
  secrets)
    (cd "$repo_root" && "$gitleaks" git --no-banner --no-color --redact --log-opts='--all' .)
    ;;
  audit)
    "$0" vuln
    "$0" secrets
    ;;
  *)
    echo "usage: $0 [vuln|secrets|audit]" >&2
    exit 2
    ;;
esac
