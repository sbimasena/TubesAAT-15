#!/usr/bin/env bash
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6
[[ -n "${TARGET_URL:-}" ]] || die 'Set TARGET_URL to the Aggregator load balancer URL (a pod port-forward cannot measure scaling)'
confirm "Run ${VUS:-10000} VU for ${DURATION:-120s} and change Aggregator replicas 3 -> 5 -> 2 in $(kubectl config current-context)?"
mkdir -p "$ROOT/artifacts/m2/runs"
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p2-load"
exec > >(tee "$out.log") 2>&1
original="$(kubectl -n "$NS" get deployment aggregator -o jsonpath='{.spec.replicas}')"
scaler=''
cleanup() {
  [[ -z "$scaler" ]] || { kill "$scaler" 2>/dev/null || true; wait "$scaler" 2>/dev/null || true; }
  run kubectl -n "$NS" scale deployment aggregator --replicas="$original"
}
trap cleanup EXIT
run kubectl -n "$NS" scale deployment aggregator --replicas=3
run kubectl -n "$NS" rollout status deployment/aggregator --timeout=120s
(
  sleep 30
  run kubectl -n "$NS" scale deployment aggregator --replicas=5
  run kubectl -n "$NS" rollout status deployment/aggregator --timeout=30s
  sleep 20
  run kubectl -n "$NS" scale deployment aggregator --replicas=2
  if [[ -n "${ROLLOUT_IMAGE:-}" ]]; then
    sleep 20
    run kubectl -n "$NS" set image deployment/aggregator "aggregator=$ROLLOUT_IMAGE"
    run kubectl -n "$NS" rollout status deployment/aggregator --timeout=40s
  fi
) &
scaler=$!
run k6 run --summary-export "$out.json" "$ROOT/tests/k6/m2-aggregator.js"
wait "$scaler"
scaler=''
echo "Evidence: $out.log and $out.json. Verify upstream instance IDs and Audit checksums separately."
