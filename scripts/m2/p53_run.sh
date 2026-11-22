#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl python3 curl
load_env
ctx="$(kubectl config current-context)"
confirm "P5.3 will temporarily restart PVMBG with 3s delay and scale Sensor Pantai to 0 in context $ctx; all settings will be restored. Continue?"
port="${P53_AUTH_LOCAL_PORT:-18084}"
if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
  echo "Port $port in use; set P53_AUTH_LOCAL_PORT." >&2; exit 2
fi
kubectl -n "$NS" port-forward svc/auth "$port:8084" >/dev/null 2>&1 &
pf=$!
trap 'kill "$pf" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$port/health" >/dev/null; then break; fi
  sleep .2
done
python3 "$ROOT/scripts/m2/p53_concurrent.py" \
  --client-api-url "${CLIENT_API_URL:-http://192.168.18.201:30080}" \
  --auth-url "http://127.0.0.1:$port" \
  --context "$ctx" --apply-faults \
  --phase-seconds "${P53_PHASE_SECONDS:-45}"
