#!/usr/bin/env bash
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6
[[ -n "${TARGET_URL:-}" ]] || die 'Set TARGET_URL to the Aggregator load balancer URL (a pod port-forward cannot measure scaling)'
if [[ "${1:-}" == throughput ]]; then
  need python3
  confirm "Measure throughput with one and three Aggregator replicas in $(kubectl config current-context)?"
  mkdir -p "$ROOT/artifacts/m2/runs"
  out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p2-throughput"
  original="$(kubectl -n "$NS" get deployment aggregator -o jsonpath='{.spec.replicas}')"
  trap 'run kubectl -n "$NS" scale deployment aggregator --replicas="$original"' EXIT
  for replicas in 1 3; do
    run kubectl -n "$NS" scale deployment aggregator --replicas="$replicas"
    run kubectl -n "$NS" rollout status deployment/aggregator --timeout=120s
    REQUIRE_ZERO_FAILURES=0 run k6 run --summary-export "$out-$replicas.json" "$ROOT/tests/k6/m2-aggregator.js"
  done
  python3 - "$out" <<'PY'
import json
from pathlib import Path
import sys

def rate(replicas):
    summary = json.loads(Path(f"{sys.argv[1]}-{replicas}.json").read_text())
    return summary["metrics"]["responses_200"]["rate"]

one, three = rate(1), rate(3)
if one <= 0:
    sys.exit("No successful responses in the one-replica baseline")
ratio = three / one
print(f"Successful responses/s: {one:.2f} at 1 replica, {three:.2f} at 3 replicas, ratio {ratio:.3f}×")
sys.exit(ratio < 2)
PY
  exit
fi
confirm "Run ${VUS:-10000} VU for ${DURATION:-120s} and change Aggregator replicas 3 -> 5 -> 2 in $(kubectl config current-context)?"
mkdir -p "$ROOT/artifacts/m2/runs"
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p2-load"
exec > >(tee "$out.log") 2>&1
original="$(kubectl -n "$NS" get deployment aggregator -o jsonpath='{.spec.replicas}')"
strict=0
if [[ -n "${ROLLOUT_IMAGE:-}" ]]; then
  current="$(kubectl -n "$NS" get deployment aggregator -o 'jsonpath={.spec.template.spec.containers[?(@.name=="aggregator")].image}')"
  [[ "$ROLLOUT_IMAGE" != "$current" ]] || die "ROLLOUT_IMAGE must differ from the deployed image"
  strict=1
fi
scaler=''
load=''
cleanup() {
  [[ -z "$scaler" ]] || { kill "$scaler" 2>/dev/null || true; wait "$scaler" 2>/dev/null || true; }
  [[ -z "$load" ]] || { kill "$load" 2>/dev/null || true; wait "$load" 2>/dev/null || true; }
  run kubectl -n "$NS" scale deployment aggregator --replicas="$original"
}
trap cleanup EXIT
run kubectl -n "$NS" scale deployment aggregator --replicas=3
run kubectl -n "$NS" rollout status deployment/aggregator --timeout=120s
echo "+ k6 run --summary-export $out.json $ROOT/tests/k6/m2-aggregator.js" >&2
REQUIRE_ZERO_FAILURES="$strict" k6 run --summary-export "$out.json" "$ROOT/tests/k6/m2-aggregator.js" &
load=$!
(
  sleep 30
  kill -0 "$load" 2>/dev/null || die "load ended before scaling to five replicas"
  run kubectl -n "$NS" scale deployment aggregator --replicas=5
  run kubectl -n "$NS" rollout status deployment/aggregator --timeout=25s
  sleep 15
  kill -0 "$load" 2>/dev/null || die "load ended before scaling to two replicas"
  run kubectl -n "$NS" scale deployment aggregator --replicas=2
  run kubectl -n "$NS" rollout status deployment/aggregator --timeout=15s
  if [[ "$strict" == 1 ]]; then
    kill -0 "$load" 2>/dev/null || die "load ended before the image rollout"
    run kubectl -n "$NS" set image deployment/aggregator "aggregator=$ROLLOUT_IMAGE"
    run kubectl -n "$NS" rollout status deployment/aggregator --timeout=30s
  fi
  kill -0 "$load" 2>/dev/null || die "load ended before the cluster changes completed"
) &
scaler=$!
wait "$load"
load=''
wait "$scaler"
scaler=''
echo "Evidence: $out.log and $out.json. Verify upstream instance IDs and Audit checksums separately."
