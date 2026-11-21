#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
for tool in python3 docker; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done
echo "+ python3 -B scripts/m2/pantai-resilience.py $*" >&2
exec python3 -B scripts/m2/pantai-resilience.py "$@"
