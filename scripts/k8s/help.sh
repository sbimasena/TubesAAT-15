#!/usr/bin/env bash
# Print Makefile targets documented with "target: ## description".
set -euo pipefail
grep -E '^[a-z0-9-]+:.*## ' "${1:-Makefile}" | sed -E 's/^([a-z0-9-]+):.*## /\1\t/' |
  while IFS=$'\t' read -r target description; do
    printf '%-18s %s\n' "$target" "$description"
  done
