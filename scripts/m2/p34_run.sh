#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl python3 docker
load_env
port="${P34_LOCAL_PORT:-18083}"
if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
  echo "Port $port is in use; set P34_LOCAL_PORT to an unused port." >&2; exit 2
fi
kubectl -n "$NS" port-forward svc/aggregator "$port:8083" >/dev/null 2>&1 &
pf=$!
trap 'kill "$pf" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  curl -sf "http://127.0.0.1:$port/internal/v1/status" >/dev/null && break
  sleep .2
done
AUDIT_URL="http://127.0.0.1:$port" bash "$ROOT/scripts/m2/p34_verify.sh"
