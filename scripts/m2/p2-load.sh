#!/usr/bin/env bash
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6
if [[ "${1:-}" == throughput ]]; then
  need python3
  # 100m keeps one replica (≈230 resp/s ≈ 10 MB/s) below the slowest node link
  # (node-b over Wi-Fi), so each replica is bounded by its own CPU.
  # Without a per-replica CPU bound the cached read path never saturates one
  # replica (≈0.5 core at 3.3k resp/s); the measurement then shows the inter-node
  # network, not scaling. Node-local NodePorts keep kube-proxy from forwarding
  # load across that link. Both changes are reverted on exit.
  cpu="${CPU_LIMIT:-100m}"
  confirm "Measure throughput at 1 and 3 Aggregator replicas (CPU limit $cpu per replica, node-local NodePorts) in $(kubectl config current-context)?"
  mkdir -p "$ROOT/artifacts/m2/runs"
  out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p2-throughput"
  original="$(kubectl -n "$NS" get deployment aggregator -o jsonpath='{.spec.replicas}')"
  port="$(kubectl -n "$NS" get service aggregator -o jsonpath='{.spec.ports[0].nodePort}')"
  restore() {
    run kubectl -n "$NS" patch service aggregator -p '{"spec":{"externalTrafficPolicy":"Cluster"}}'
    run kubectl -n "$NS" patch deployment aggregator --type=json -p '[{"op":"remove","path":"/spec/template/spec/containers/0/resources/limits/cpu"}]' || true
    run kubectl -n "$NS" scale deployment aggregator --replicas="$original"
  }
  trap restore EXIT
  # Hosts of Running replicas that are not terminating (a terminating pod still serves).
  hosts() {
    kubectl -n "$NS" get pods -l app=aggregator -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.deletionTimestamp}{" "}{.status.hostIP}{"\n"}{end}' | awk 'NF == 1 {print $1}'
  }
  run kubectl -n "$NS" patch service aggregator -p '{"spec":{"externalTrafficPolicy":"Local"}}'
  run kubectl -n "$NS" set resources deployment/aggregator --limits=cpu="$cpu"
  for replicas in 1 3; do
    run kubectl -n "$NS" scale deployment aggregator --replicas="$replicas"
    run kubectl -n "$NS" rollout status deployment/aggregator --timeout=300s
    for _ in $(seq 60); do [[ "$(hosts | wc -l)" -eq "$replicas" ]] && break; sleep 5; done
    urls="$(hosts | sort -u | sed "s|.*|http://&:$port|" | paste -sd, -)"
    echo "+ $replicas replica(s) behind $urls" >&2
    # A failed k6 threshold (for example a slow one-replica baseline) must not
    # abort the comparison; the summary is still exported for the ratio below.
    TARGET_URL="$urls" VUS="${VUS:-100}" DURATION="${DURATION:-60s}" REQUIRE_ZERO_FAILURES=0 k6 run --summary-export "$out-$replicas.json" "$ROOT/tests/k6/m2-aggregator.js" || true
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
[[ -n "${TARGET_URL:-}" ]] || die 'Set TARGET_URL to the Aggregator NodePort URL (http://<node-ip>:30083)'
# 180 s: a pod scheduled on node-b (Wi-Fi) needs ~30 s to become Ready, so 3→5→2 plus
# the image rollout do not fit inside 120 s of load. The spec only requires ≥120 s.
export DURATION="${DURATION:-180s}"
confirm "Run ${VUS:-10000} VU for $DURATION and change Aggregator replicas 3 -> 5 -> 2 in $(kubectl config current-context)?"
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
  run kubectl -n "$NS" rollout status deployment/aggregator --timeout=60s
  sleep 15
  kill -0 "$load" 2>/dev/null || die "load ended before scaling to two replicas"
  run kubectl -n "$NS" scale deployment aggregator --replicas=2
  run kubectl -n "$NS" rollout status deployment/aggregator --timeout=30s
  if [[ "$strict" == 1 ]]; then
    kill -0 "$load" 2>/dev/null || die "load ended before the image rollout"
    run kubectl -n "$NS" set image deployment/aggregator "aggregator=$ROLLOUT_IMAGE"
    run kubectl -n "$NS" rollout status deployment/aggregator --timeout=60s
  fi
  kill -0 "$load" 2>/dev/null || die "load ended before the cluster changes completed"
) &
scaler=$!
wait "$load"
load=''
wait "$scaler"
scaler=''
echo "Evidence: $out.log and $out.json. Verify upstream instance IDs and Audit checksums separately."
