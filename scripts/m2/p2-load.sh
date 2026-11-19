#!/usr/bin/env bash
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6
[[ -n "${TARGET_URL:-}" ]] || die 'Set TARGET_URL to the Aggregator load balancer URL (a pod port-forward cannot measure scaling)'
confirm "Run 10k VU and change Aggregator replicas 3 -> 5 -> 2 in $(kubectl config current-context)?"
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
