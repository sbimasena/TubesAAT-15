# Shared helpers for scripts/k8s/*.sh. Source it; do not execute it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NS=sistem-informasi-kebencanaan-bnpb
TESTER_NS=tester-agent
TESTER_MANIFEST=https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml
GENERATED="$ROOT/deploy/k8s/.generated"

# run prints the exact command before running it (context-m2.md §8.2).
run() {
  echo "+ $*" >&2
  "$@"
}

die() {
  echo "error: $*" >&2
  exit 1
}

need() {
  for tool in "$@"; do
    command -v "$tool" >/dev/null || die "missing tool: $tool"
  done
}

# confirm guards destructive actions; YES=1 skips the prompt.
confirm() {
  [[ "${YES:-}" == 1 ]] && return 0
  read -r -p "$1 [y/N] " answer
  [[ "$answer" == y || "$answer" == Y ]] || die "aborted"
}

# load_env exports ENV_FILE (default .env), tolerating CRLF line endings.
load_env() {
  local file="${ENV_FILE:-$ROOT/.env}"
  [[ -f "$file" ]] || die "$file not found; copy .env.example to .env and fill in the secrets"
  set -a
  # shellcheck disable=SC1090
  source <(sed 's/\r$//' "$file")
  set +a
}

# sha256 hashes stdin to hex (macOS ships shasum, not sha256sum).
sha256() {
  if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1
}

secret_value() {
  kubectl -n "$NS" get secret "$1" -o "jsonpath={.data.$2}" | base64 -d
}
