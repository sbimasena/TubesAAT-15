#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl python3 curl
jaeger_port="${P57_JAEGER_PORT:-16686}"
prom_port="${P57_PROM_PORT:-19090}"
for port in "$jaeger_port" "$prom_port"; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
    echo "Port $port in use; choose another P57_*_PORT." >&2; exit 2
  fi
done
kubectl -n observability port-forward svc/jaeger "$jaeger_port:16686" >/dev/null 2>&1 &
pf1=$!
kubectl -n observability port-forward svc/prometheus "$prom_port:9090" >/dev/null 2>&1 &
pf2=$!
trap 'kill "$pf1" "$pf2" 2>/dev/null || true' EXIT
for _ in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$jaeger_port/api/services" >/dev/null \
    && curl -sf "http://127.0.0.1:$prom_port/-/ready" >/dev/null; then break; fi
  sleep .2
done
python3 "$ROOT/scripts/m2/p57_evidence.py" \
  --jaeger-url "http://127.0.0.1:$jaeger_port" \
  --prometheus-url "http://127.0.0.1:$prom_port" \
  --since-minutes "${P57_SINCE_MINUTES:-20}"
