#!/usr/bin/env bash
source "$(dirname "$0")/../k8s/lib.sh"
need go python3
cd "$ROOT"
mkdir -p artifacts/m2/runs
log="artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p4-check.log"
exec > >(tee "$log") 2>&1
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  echo "TEST_DATABASE_URL is unset: database recovery tests will be skipped; this is not a database acceptance pass."
fi
run go test -race -p 1 -count=1 -v ./services/aggregator/internal/satellite ./services/aggregator/internal/store
run python3 -B -m unittest discover -s tests -p 'test_satellite_*.py' -v
echo "Evidence: $log. These tests do not claim live feed, 60x, or physical node-failure acceptance."
