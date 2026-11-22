#!/usr/bin/env bash
# P3.4: copy the parked PVMBG DLQ rows out of the cluster (read-only SELECT) and
# replay the copy through the production replay path on a disposable PostgreSQL.
# The cluster DLQ is never changed.
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl docker
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p34-replay-copy"
mkdir -p "$out"
sql="SELECT json_build_object('mid', message_id, 'ref', source_ref_id, 'at', received_at,
  'raw', convert_from(raw, 'UTF8')) FROM unprocessed_messages WHERE source='PVMBG' AND status='parked';"
for pod in $(kubectl -n "$NS" get pods -l app=store -o name); do
  # Every replica of a row is copied; the test keeps one row per message_id.
  printf '%s\n' "$sql" | kubectl -n "$NS" exec -i "${pod#pod/}" -- sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At'
done > "$out/parked-raw.jsonl"
echo "+ copied $(wc -l < "$out/parked-raw.jsonl") replica rows to $out/parked-raw.jsonl" >&2
net="p34-$$"; pg="p34pg-$$"
cleanup() { docker rm -f "$pg" >/dev/null 2>&1; docker network rm "$net" >/dev/null 2>&1; }
trap cleanup EXIT
docker network create "$net" >/dev/null
docker run -d --rm --name "$pg" --network "$net" -e POSTGRES_PASSWORD=test postgres:17-alpine >/dev/null
for _ in $(seq 30); do docker exec "$pg" pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
docker run --rm --network "$net" -v gomod:/go -v "$ROOT/services/aggregator:/src" -v "$out:/out:ro" -w /src \
  -e TEST_DATABASE_URL="postgres://postgres:test@$pg:5432/postgres?sslmode=disable" -e P34_PARKED_FIXTURE=/out/parked-raw.jsonl \
  golang:1.27.1 go test -race -count=1 -v -run 'TestPVMBGReplayParkedCopy|TestPVMBGReplayKeepsSourceOrder' ./internal/ingest/ \
  | tee "$out/go-test.log"
status=${PIPESTATUS[0]}
echo "Evidence: $out" >&2
exit "$status"
