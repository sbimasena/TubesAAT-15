#!/usr/bin/env bash
# P5.5 priority under overload: Media floods the Client API while the Field Team keeps
# reading. Records the client-api HPA before and after (bonus: capacity that adapts).
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl k6
load_env
API_URL="${API_URL:-http://192.168.18.201:30080}"
confirm "Run ${MEDIA_VUS:-400} Media VU + ${FIELD_VUS:-20} Field Team VU against $API_URL for ${DURATION:-60s}?"
mkdir -p "$ROOT/artifacts/m2/runs"
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p5-priority"
exec > >(tee "$out.log") 2>&1
kubectl -n "$NS" port-forward svc/auth 18084:8084 >/dev/null &
forward=$!
trap 'kill "$forward" 2>/dev/null || true' EXIT
for _ in $(seq 50); do curl -fsS -o /dev/null http://127.0.0.1:18084/health 2>/dev/null && break; sleep 0.2; done
run kubectl -n "$NS" get hpa client-api
# Passwords come from .env (the same values deploy.sh put in the auth-credentials Secret).
AUTH_URL=http://127.0.0.1:18084 API_URL="$API_URL" MEDIA_PASSWORD="$MEDIA_CLIENT_PASSWORD" \
  FIELD_TEAM_PASSWORD="$FIELD_TEAM_CLIENT_PASSWORD" \
  k6 run --quiet --summary-export "$out.json" "$ROOT/tests/k6/p5-priority.js" 2>&1 | grep -v 'Request Failed' || true
run kubectl -n "$NS" get hpa client-api
run kubectl -n "$NS" get pods -l app=client-api -o wide
echo "Evidence: $out.log and $out.json"
