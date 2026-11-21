#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
command -v python3 >/dev/null || { echo "missing tool: python3" >&2; exit 1; }
echo "+ python3 -B scripts/m2/slo-check.py $*" >&2
exec python3 -B scripts/m2/slo-check.py "$@"
