#!/usr/bin/env bash
# P5.6 game day: combined, unannounced-style failures under load with no operator action
# during the recovery window. Faults end on a fixed schedule; the system must recover by
# itself. Samples /internal/v1/status every 10 s and compares Audit checksums before/after.
#
#   t+0    load: p5-priority (Media flood + Field Team) for GAMEDAY_SECONDS
#   t+20   Tester Agent kill-store (practice)
#   t+40   Sensor Pantai scaled to 0 (sensor silent)
#   t+60   PVMBG outage on
#   t+150  faults end: Sensor Pantai back to 1, PVMBG outage off
#   end    recovery checks: sources fresh, breaker closed, storage serving, partial:false
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6 curl python3
load_env
total="${GAMEDAY_SECONDS:-240}"
AGG="${AGG_URL:-http://192.168.18.201:30083}"
confirm "Run a ${total}s game day (kill-store, sensor down, PVMBG outage, load) in $(kubectl config current-context)?"
mkdir -p "$ROOT/artifacts/m2/runs"
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p5-gameday"
exec > >(tee "$out.log") 2>&1
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  kubectl -n "$NS" scale deploy/sensor-pantai --replicas=1 >/dev/null 2>&1 || true
}
trap cleanup EXIT

checksums() {
  for source in BMKG PVMBG SATELIT SENSOR_PANTAI; do
    curl -s -H "Authorization: Bearer $AUDIT_TOKEN" "$AGG/internal/audit/checksum?source=$source" |
      python3 -c 'import json,sys; d=json.load(sys.stdin); print(f"  {d[\"source\"]}: count={d[\"count\"]} partial={d[\"partial\"]} unavailable={d.get(\"unavailable_units\")}")' ||
      echo "  $source: unavailable"
  done
}
sample() {
  curl -s -m 5 "$AGG/internal/v1/status" | python3 -c '
import json, sys, time
d = json.load(sys.stdin)
src = {k: ("stale" if v.get("stale") else "ok") for k, v in d.get("sources", {}).items()}
units = {u["unit"]: ("ok" if u.get("reachable") and u.get("serving") else "DOWN") for u in d.get("storage", {}).get("units", [])}
breaker = (d.get("coastal") or {}).get("breaker", {}).get("state", "?")
print(time.strftime("%H:%M:%S", time.gmtime()), "sources", src, "units", units, "breaker", breaker,
      "under_replicated", d.get("storage", {}).get("under_replicated_partitions"))' 2>/dev/null || echo "$(date -u +%T) status unavailable"
}
pvmbg_outage() {
  local port=18282
  kubectl -n "$NS" port-forward svc/pvmbg "$port:8082" >/dev/null 2>&1 &
  local pf=$!
  for _ in $(seq 50); do curl -s -o /dev/null "http://127.0.0.1:$port/health" && break; sleep 0.2; done
  echo "+ PVMBG outage $1"
  curl -s -X POST "http://127.0.0.1:$port/admin/outage" -H "Content-Type: application/json" \
    -H "Authorization: Bearer $(secret_value source-credentials PVMBG_TOKEN)" -d "{\"enabled\":$1}"
  echo
  kill "$pf" 2>/dev/null || true
}

echo "== before"; checksums; sample
DURATION="${total}s" MEDIA_VUS="${MEDIA_VUS:-300}" FIELD_VUS="${FIELD_VUS:-20}" YES=1 \
  bash "$ROOT/scripts/m2/p5-priority.sh" > "$out-load.log" 2>&1 &
pids+=($!)
( while true; do sample; sleep 10; done ) &
pids+=($!)
sleep 20; echo "+ t+20 Tester kill-store"; MSYS_NO_PATHCONV=1 kubectl exec -n "$TESTER_NS" daemonset/tester-agent -- /app run practice kill-store | tail -2
sleep 20; echo "+ t+40 Sensor Pantai -> 0"; kubectl -n "$NS" scale deploy/sensor-pantai --replicas=0
sleep 20; echo "+ t+60"; pvmbg_outage true
sleep 90; echo "+ t+150 faults end"; kubectl -n "$NS" scale deploy/sensor-pantai --replicas=1; pvmbg_outage false
wait "${pids[0]}" || true
sleep 30
echo "== after (no operator action since t+0)"; checksums; sample
echo "== load result (Field Team vs Media)"; grep -E "field_ok|field_latency|media_controlled|uncontrolled_errors|REPLICAS|client-api " "$out-load.log" | head -12
echo "Evidence: $out.log, $out-load.log"
