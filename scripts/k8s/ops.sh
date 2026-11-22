#!/usr/bin/env bash
# Day-to-day cluster operations behind the Makefile: ops.sh <command> [arg].
source "$(dirname "$0")/lib.sh"
need kubectl

FORWARDS=()
AUTH_FILES=()
cleanup() {
  for pid in "${FORWARDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  for file in "${AUTH_FILES[@]}"; do rm -f -- "$file"; done
}
trap cleanup EXIT

# curl reads the header from a short-lived private file. Keep bearer credentials
# out of curl argv and logs, including when a request fails.
curl_auth() {
  local token="$1"
  shift
  local header_file status
  [[ "$token" != *$'\n'* && "$token" != *$'\r'* ]] || die "token contains an invalid header line break"
  header_file="$(mktemp "${TMPDIR:-/tmp}/bnpb-curl-auth.XXXXXX")"
  AUTH_FILES+=("$header_file")
  chmod 600 "$header_file"
  printf 'Authorization: Bearer %s\n' "$token" >"$header_file"
  if curl --header "@$header_file" "$@"; then
    status=0
  else
    status=$?
  fi
  rm -f -- "$header_file"
  return "$status"
}

# forward runs a background port-forward and waits until the local port answers.
forward() {
  local target="$1" ports="$2"
  if (exec 3<>"/dev/tcp/127.0.0.1/${ports%%:*}") 2>/dev/null; then
    die "local port ${ports%%:*} is already in use"
  fi
  echo "+ kubectl -n $NS port-forward $target $ports" >&2
  kubectl -n "$NS" port-forward "$target" "$ports" >/dev/null &
  local pid=$!
  FORWARDS+=("$pid")
  for _ in $(seq 50); do
    kill -0 "$pid" 2>/dev/null || break
    (exec 3<>"/dev/tcp/127.0.0.1/${ports%%:*}") 2>/dev/null && kill -0 "$pid" 2>/dev/null && return 0
    sleep 0.2
  done
  die "port-forward to $target did not become ready"
}

# broker_ctl runs rabbitmqctl on the first broker pod that answers; member names are shortened.
broker_ctl() {
  local pod
  for pod in $(kubectl -n "$NS" get pods -l app=rabbitmq -o 'jsonpath={.items[*].metadata.name}'); do
    kubectl -n "$NS" exec "$pod" -- rabbitmqctl -q "$@" 2>/dev/null | sed "s/\.rabbitmq-headless\.$NS\.svc\.cluster\.local//g" && return 0
  done
  die "tidak ada pod broker yang menjawab"
}

case "${1:-}" in
  status)
    run kubectl get nodes -o wide
    run kubectl -n "$NS" get pods -o wide -L role
    run kubectl -n "$TESTER_NS" get pods -o wide
    ;;
  logs)
    [[ -n "${2:-}" ]] || die "usage: make logs S=<app>, e.g. S=aggregator"
    run kubectl -n "$NS" logs -l "app=$2" -f --prefix --tail=100 --max-log-requests=10
    ;;
  # MSYS_NO_PATHCONV stops Git Bash on Windows from rewriting "/app" into a Windows path.
  tester-list)
    run env MSYS_NO_PATHCONV=1 kubectl -n "$TESTER_NS" exec daemonset/tester-agent -- /app list
    ;;
  tester)
    [[ -n "${2:-}" ]] || die "usage: make tester S=<scenario>; see make tester-list"
    confirm "Run Tester Agent practice scenario '$2' against $(kubectl config current-context)?"
    run env MSYS_NO_PATHCONV=1 kubectl -n "$TESTER_NS" exec daemonset/tester-agent -- /app run practice "$2"
    run kubectl -n "$TESTER_NS" logs -l app=tester-agent --since=10m --prefix --max-log-requests=10
    ;;
  open)
    forward svc/rabbitmq 15672:15672
    echo "RabbitMQ management: http://127.0.0.1:15672"
    echo "Client API: http://127.0.0.1:8080 (k3d) or http://<node-ip>:30080 (cluster)"
    echo "Press Ctrl+C to close the port-forwards."
    wait
    ;;
  reset)
    # Back to the deployed manifests (replica counts, images) and normal mock modes.
    [[ -f "$GENERATED/kustomization.yaml" ]] || die "nothing deployed from this checkout yet; run make deploy"
    # Another member may have deployed since; re-applying our older images would silently roll back.
    ours="$(sed -n 's#.*name: bnpb/aggregator, newName: \(.*\), newTag: "\(.*\)"}#\1:\2#p' "$GENERATED/kustomization.yaml")"
    running="$(kubectl -n "$NS" get deployment aggregator -o jsonpath='{.spec.template.spec.containers[0].image}')"
    [[ "$ours" == "$running" ]] || die "cluster runs $running but this checkout last deployed $ours; run make deploy instead"
    run kubectl apply -k "$GENERATED"
    token="$(secret_value source-credentials PVMBG_TOKEN)"
    forward svc/pvmbg 18082:8082
    for call in "schema-version {\"version\":1}" "outage {\"enabled\":false}"; do
      echo "+ curl -X POST http://127.0.0.1:18082/admin/${call%% *} -d '${call#* }'" >&2
      curl_auth "$token" -fsS -X POST "http://127.0.0.1:18082/admin/${call%% *}" \
        -H "Content-Type: application/json" -d "${call#* }"
      echo
    done
    ;;
  broker-status)
    # D-BROKER evidence: running members, then each queue's type, leader and live replicas.
    echo "+ rabbitmqctl eval 'rabbit_nodes:list_running().'" >&2
    broker_ctl eval 'rabbit_nodes:list_running().'
    echo "+ rabbitmqctl list_queues name type leader online messages" >&2
    broker_ctl list_queues name type leader online messages
    ;;
  broker-recover)
    # A member wiped by wipe-node rejoins by itself (blank disk, same name) and its quorum
    # replicas catch up. Only a member that crashed while half-joined (CrashLoopBackOff, e.g.
    # incompatible_feature_flags or failed_to_cluster_khepri_node) needs its disk emptied again.
    [[ "${2:-}" =~ ^rabbitmq-[0-9]+$ ]] || die "usage: make broker-recover S=rabbitmq-N"
    confirm "Kosongkan disk $2 agar bergabung ulang dari nol? Queue tetap ada di 2 member lain."
    run kubectl -n "$NS" delete pvc "data-$2" --wait=false
    run kubectl -n "$NS" delete pod "$2"
    run kubectl -n "$NS" rollout status statefulset/rabbitmq --timeout=600s
    bash "$0" broker-status
    ;;
  system-status)
    # Aggregator's GET /internal/v1/status: storage, sources, and each owner's section.
    forward svc/aggregator 18083:8083
    echo "+ curl http://127.0.0.1:18083/internal/v1/status" >&2
    curl -fsS http://127.0.0.1:18083/internal/v1/status
    echo
    ;;
  obs-open)
    # Metrics dashboard and traces (context-m2.md §8.1); the analytics dashboard is p1-dashboard.
    NS=observability forward svc/grafana 3000:3000
    NS=observability forward svc/jaeger 16686:16686
    echo "Grafana (BNPB Overview): http://127.0.0.1:3000"
    echo "Jaeger (trace per request, cari service aggregator): http://127.0.0.1:16686"
    echo "Press Ctrl+C to close the port-forwards."
    wait
    ;;
  p2-kill-leader)
    # Kill the actual leader pod by reading the Lease holder, instead of relying
    # on the Tester Agent to pick it among the Aggregator pods.
    holder="$(kubectl -n "$NS" get lease aggregator-leader -o jsonpath='{.spec.holderIdentity}')"
    [[ -n "$holder" ]] || die "Aggregator has no elected leader yet"
    pod="${holder%%_*}"
    confirm "Delete leader pod $pod in $(kubectl config current-context)?"
    run kubectl -n "$NS" delete pod "$pod"
    ;;
  p4-export)
    need python3
    token="$(secret_value audit-token token)"
    [[ -n "$token" ]] || die "Audit token is empty"
    forward svc/aggregator 18083:8083
    AUDIT_TOKEN="$token" python3 "$ROOT/scripts/m2/export-satellite.py"
    ;;
  p1-checksum)
    # Audit API checksum per source and over all sources (kontrak-m2.md §7.3).
    token="$(secret_value audit-token token)"
    forward svc/aggregator 18083:8083
    for source in BMKG PVMBG SATELIT SENSOR_PANTAI ""; do
      echo "+ curl http://127.0.0.1:18083/internal/audit/checksum?source=$source" >&2
      curl_auth "$token" -fsS "http://127.0.0.1:18083/internal/audit/checksum?source=$source"
    done
    ;;
  p1-distribution)
    # Rows per store unit: with RF=2 over 3 units each should hold ~2/3, never all (P1.1).
    for pod in $(kubectl -n "$NS" get pods -l app=store -o name); do
      run kubectl -n "$NS" exec -i "$pod" -c postgres -- \
        sh -c 'psql -qAtF " | " -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL' || echo "tidak terjangkau"
SELECT current_setting('bnpb.node_name', true), count(*), count(DISTINCT partition_id) FROM hazard_events;
SQL
    done
    echo "kolom: node | rows | partitions (bandingkan rows dengan count di make p1-checksum)"
    # The reconciler's placement; "adding"/"removing" mark a move in progress.
    run kubectl -n "$NS" get configmap store-partition-map -o 'jsonpath={.data.map\.json}'
    echo
    # P1.1 (revised 20 Nov): a "unit" is a partition; report primary/replica per node and
    # that no unit keeps both copies on one node. replicas[0] is the primary.
    nodes="$(kubectl -n "$NS" get pods -l app=store -o 'jsonpath={range .items[*]}{.metadata.name}={.spec.nodeName} {end}')"
    kubectl -n "$NS" get configmap store-partition-map -o 'jsonpath={.data.map\.json}' |
      python3 -c '
import collections, json, sys
nodes = dict(x.split("=") for x in sys.argv[1].split())
parts = json.load(sys.stdin)["partitions"]
primary, replica = collections.Counter(), collections.Counter()
same = 0
for p in parts:
    units = p["replicas"]
    primary[units[0]] += 1
    replica.update(units[1:])
    same += len({nodes.get(u) for u in units}) < 2
print("store | node | partisi primary | partisi replica")
for u in sorted(nodes):
    print(f"{u} | {nodes[u]} | {primary[u]} | {replica[u]}")
print(f"partisi (unit) dengan primary dan replica di node yang sama: {same} dari {len(parts)}")
' "$nodes"
    ;;
  p1-dashboard)
    forward svc/aggregator 18083:8083
    echo "Dashboard analitik: http://127.0.0.1:18083/dashboard (Ctrl+C untuk menutup)"
    wait
    ;;
  p1-wipe-node)
    # P1.4: record the checksum first; the node only comes back after a manual power-on.
    bash "$0" p1-checksum
    bash "$0" tester wipe-node
    echo "Setelah node kembali Ready: make p1-distribution lalu make p1-checksum; count dan checksum harus sama dengan di atas."
    ;;
  p1-amnesia)
    # P1.5 without a VM: empty one unit's disk and give it a new incarnation, as a wiped node
    # rejoining would. The reconciler must stop reading it, refill it, then serve again.
    [[ "${2:-}" =~ ^store-[0-9]+$ ]] || die "usage: make p1-amnesia S=store-N"
    confirm "Kosongkan data $2 (simulasi disk kosong) di $(kubectl config current-context)?"
    run kubectl -n "$NS" exec -i "pod/$2" -c postgres -- \
      sh -c 'psql -v ON_ERROR_STOP=1 -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
TRUNCATE hazard_events, hazard_outbox, field_reports, unprocessed_messages;
UPDATE unit_meta SET incarnation = gen_random_uuid() RETURNING 'incarnation baru: ' || incarnation;
SQL
    echo "Lihat log: make logs S=aggregator ('lost its data' lalu 'refilled'), lalu make p1-checksum dan make p1-distribution."
    ;;
  p1-netpol)
    # Storage ownership (context-m2.md §6.4): a pod labelled like client-api must not reach
    # the store; the same pod labelled role=aggregator must. Only labels differ, so the
    # NetworkPolicy, not DNS or credentials, is what blocks the first.
    # pg_isready exits 0 only on a real PostgreSQL answer (image already on every node).
    # The sleep lets kube-router add the new pod's IP to the policy before the probe.
    check() {
      run kubectl -n "$NS" run "p1-netpol-$RANDOM" --rm -i --restart=Never --quiet --image=postgres:17-alpine \
        --labels="$1" --command -- sh -c 'sleep 5; pg_isready -h store-0.store -p 5432 -t 3'
    }
    if check app=netpol-check,role=api; then die "role=api reached store-0:5432; NetworkPolicy store-ingress is not enforced"; fi
    echo "role=api -> store-0:5432 ditolak (benar)"
    check app=netpol-check,role=aggregator || die "role=aggregator could not reach store-0:5432"
    echo "role=aggregator -> store-0:5432 terhubung (benar)"
    ;;
  p1-audit)
    # The other two Audit API endpoints (kontrak-m2.md §7): a cursor page, then where one event lives.
    token="$(secret_value audit-token token)"
    forward svc/aggregator 18083:8083
    page="$(curl_auth "$token" -fsS "http://127.0.0.1:18083/internal/audit/events?source=BMKG&limit=2")"
    echo "events: $page"
    id="$(grep -o '"hazard_id":"[^"]*"' <<<"$page" | head -1 | cut -d'"' -f4)"
    [[ -n "$id" ]] || die "tidak ada event BMKG untuk dicari lokasinya"
    echo "location $id: $(curl_auth "$token" -fsS "http://127.0.0.1:18083/internal/audit/location?hazard_id=$id")"
    echo "harapan: 2 lokasi (primary + replica) di node berbeda"
    ;;
  exam-keys)
    # Exam materials from the assistants (docs/m2/demo-ujian.md). Each is optional; files, not argv.
    [[ -n "${TESTER_KEY:-}${PANTAI_KEY:-}${AUDIT_TOKEN_FILE:-}" ]] || die "usage: make exam-keys TESTER_KEY=<file> PANTAI_KEY=<file> AUDIT_TOKEN_FILE=<file>"
    if [[ -n "${TESTER_KEY:-}" ]]; then
      [[ -f "$TESTER_KEY" ]] || die "$TESTER_KEY not found"
      # Keeps the assistants' file name; the DaemonSet mounts the Secret at /var/run/tester.
      kubectl -n "$TESTER_NS" create secret generic tester-key --from-file="$TESTER_KEY" --dry-run=client -o yaml | kubectl apply -f -
      run kubectl -n "$TESTER_NS" rollout restart ds/tester-agent
      run kubectl -n "$TESTER_NS" rollout status ds/tester-agent --timeout=180s
    fi
    if [[ -n "${PANTAI_KEY:-}" ]]; then
      [[ -f "$PANTAI_KEY" ]] || die "$PANTAI_KEY not found"
      kubectl -n "$NS" create secret generic pantai-key --from-file=key="$PANTAI_KEY" --dry-run=client -o yaml | kubectl apply -f -
      echo "pantai-key terpasang; lanjutkan dengan make exam-pantai"
    fi
    if [[ -n "${AUDIT_TOKEN_FILE:-}" ]]; then
      [[ -f "$AUDIT_TOKEN_FILE" ]] || die "$AUDIT_TOKEN_FILE not found"
      token="$(tr -d '\r\n' <"$AUDIT_TOKEN_FILE")"
      [[ -n "$token" ]] || die "audit token file is empty"
      kubectl -n "$NS" create secret generic audit-token --from-literal=token="$token" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
      # AUDIT_TOKEN is read from env at start, so the new token needs new pods.
      run kubectl -n "$NS" rollout restart deploy/aggregator
      run kubectl -n "$NS" rollout status deploy/aggregator --timeout=300s
    fi
    ;;
  exam-pantai)
    # Sensor Pantai to the exam profile with the assistants' key (kontrak-m2.md §6). make deploy/reset reverts it.
    kubectl -n "$NS" get secret pantai-key >/dev/null || die "Secret pantai-key belum ada; make exam-keys PANTAI_KEY=<file>"
    confirm "Switch Sensor Pantai to PANTAI_PROFILE=exam on $(kubectl config current-context)?"
    if ! kubectl -n "$NS" get deploy sensor-pantai -o jsonpath='{.spec.template.spec.volumes[*].name}' | grep -qw key; then
      run kubectl -n "$NS" patch deploy sensor-pantai --type=json -p='[
        {"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"key","secret":{"secretName":"pantai-key"}}},
        {"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"key","mountPath":"/var/run/pantai","readOnly":true}}]'
    fi
    run kubectl -n "$NS" set env deploy/sensor-pantai PANTAI_PROFILE=exam
    run kubectl -n "$NS" rollout status deploy/sensor-pantai --timeout=180s
    echo "Aggregator mendeteksi sesi baru sendiri: make logs S=aggregator, cari 'coastal session restarted'."
    ;;
  exam-satellite-prep)
    # The satellite worker resumes from its stored offset and rejects gaps; an exam session may
    # restart offsets. Back up and clear practice SATELIT state with every Aggregator stopped,
    # so no leader rewrites the checkpoint. Leaves Aggregator at 0 until make exam-start.
    confirm "Stop Aggregator and delete practice SATELIT data + checkpoint on $(kubectl config current-context)?"
    stores="$(kubectl -n "$NS" get pods -l app=store -o 'jsonpath={range .items[*]}{.metadata.name}={.status.containerStatuses[0].ready} {end}')"
    [[ "$(grep -o '=true' <<<"$stores" | wc -l)" -eq 3 ]] || die "semua store harus Ready: $stores"
    replicas="$(kubectl -n "$NS" get deploy aggregator -o jsonpath='{.spec.replicas}')"
    kubectl -n "$NS" annotate deploy aggregator bnpb/exam-replicas="$replicas" --overwrite >/dev/null
    run kubectl -n "$NS" scale deploy aggregator --replicas=0
    run kubectl -n "$NS" wait --for=delete pod -l app=aggregator --timeout=180s || true
    backup="$ROOT/artifacts/m2/exam/$(date -u +%Y%m%dT%H%M%SZ)-satellite-practice"
    mkdir -p "$backup"
    for pod in $(kubectl -n "$NS" get pods -l app=store -o 'jsonpath={.items[*].metadata.name}'); do
      kubectl -n "$NS" exec "$pod" -c postgres -- sh -c 'psql -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "COPY (SELECT row_to_json(e) FROM hazard_events e WHERE source = '"'SATELIT'"') TO STDOUT"' >"$backup/$pod-events.ndjson"
      kubectl -n "$NS" exec "$pod" -c postgres -- sh -c 'psql -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "COPY (SELECT row_to_json(c) FROM ingest_cursor c WHERE source IN ('"'SATELIT','SATELIT_DELTA'"')) TO STDOUT"' >"$backup/$pod-cursor.ndjson"
      echo "$pod: $(wc -l <"$backup/$pod-events.ndjson") event SATELIT dicadangkan"
      run kubectl -n "$NS" exec -i "$pod" -c postgres -- sh -c 'psql -v ON_ERROR_STOP=1 -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL'
BEGIN;
DELETE FROM hazard_outbox WHERE hazard_id IN (SELECT hazard_id FROM hazard_events WHERE source = 'SATELIT');
DELETE FROM hazard_events WHERE source = 'SATELIT';
DELETE FROM ingest_cursor WHERE source IN ('SATELIT', 'SATELIT_DELTA');
COMMIT;
SQL
    done
    echo "Cadangan: $backup"
    echo "Aggregator berhenti. Mulai rekam, lalu: make exam-start EXAM_TOKEN_FILE=<file>"
    ;;
  exam-start)
    # Starts the exam satellite session (call when recording starts), then brings Aggregator back.
    [[ -f "${EXAM_TOKEN_FILE:-}" ]] || die "usage: make exam-start EXAM_TOKEN_FILE=<file berisi token ujian>"
    confirm "POST /v1/exam/start now? Only once per recording."
    curl_auth "$(tr -d '\r\n' <"$EXAM_TOKEN_FILE")" -sS -X POST -w '\nHTTP %{http_code}\n' https://satellite.naufarrel.dev/v1/exam/start
    replicas="$(kubectl -n "$NS" get deploy aggregator -o jsonpath='{.metadata.annotations.bnpb/exam-replicas}')"
    run kubectl -n "$NS" scale deploy aggregator --replicas="${replicas:-3}"
    run kubectl -n "$NS" rollout status deploy/aggregator --timeout=300s
    curl_auth "$(secret_value satellite-token token)" -sS -w '\n' https://satellite.naufarrel.dev/v1/session
    ;;
  exam-tester)
    # Exam scenarios need the tester-key Secret; check the exact command with tester-list once it is mounted.
    [[ -n "${2:-}" ]] || die "usage: make exam-tester S=<skenario ujian>; lihat make tester-list"
    confirm "Run Tester Agent EXAM scenario '$2' against $(kubectl config current-context)?"
    run env MSYS_NO_PATHCONV=1 kubectl -n "$TESTER_NS" exec daemonset/tester-agent -- /app run exam "$2"
    echo "Pantau: kubectl logs -n $TESTER_NS -l app=tester-agent -f --prefix"
    ;;
  video-submit|video-status)
    # Video link goes to the satellite service with the group token (kontrak-m2.md §5).
    token="$(secret_value satellite-token token)"
    [[ -n "$token" ]] || die "Secret satellite-token kosong"
    if [[ "$1" == video-submit ]]; then
      [[ "${2:-}" =~ ^https:// ]] || die "usage: make video-submit URL=https://(youtube|drive.google.com)/..."
      body="$(printf '{"url":"%s"}' "$2")"
      curl_auth "$token" -sS -X POST -H 'Content-Type: application/json' -d "$body" -w '\nHTTP %{http_code}\n' https://satellite.naufarrel.dev/v1/video
    fi
    curl_auth "$token" -sS -w '\nHTTP %{http_code}\n' https://satellite.naufarrel.dev/v1/video
    ;;
  audit-run)
    # Practice auditor: Audit API before/after one Tester Agent scenario, then a verdict.
    [[ -n "${2:-}" ]] || die "usage: make audit-run S=<scenario>, e.g. S=kill-store"
    [[ "$2" != wipe-node ]] || die "wipe-node mematikan node berulang (docs/m2/temuan-tester-agent.md); pakai make p1-amnesia"
    confirm "Run practice scenario '$2' with Audit API checks against $(kubectl config current-context)?"
    py=python3
    "$py" -c '' 2>/dev/null || py=python # Windows ships a python3 stub that only opens the Store
    AUDIT_TOKEN="$(secret_value audit-token token)" "$py" -B "$ROOT/scripts/m2/audit-run.py" "$2"
    ;;
  p1-load|p1-surge)
    # Load a catalog (P1.1) or a one-region surge (P1.2) and measure the writes each store
    # unit absorbed; with hash(hazard_id) placement the deltas stay even despite skewed data.
    work="$(mktemp -d)"
    if [[ "$1" == p1-load ]]; then
      [[ -f "${2:-}" ]] || die "usage: make p1-load FILE=<catalog.ndjson|catalog.json> (field BMKG: event_id, region_name, epicenter_lat, epicenter_lon, magnitude, depth_km, occurred_at)"
      if [[ "$(head -c 200 "$2" | tr -d ' \t\r\n' | head -c 1)" == "[" ]]; then
        need jq
        jq -c '.[]' "$2" >"$work/events"
      else
        cp "$2" "$work/events"
      fi
    else
      # Synthetic aftershock swarm around one epicentre (default Palu, Sulawesi Tengah).
      awk -v n="${2:-3000}" -v region="${3:-Sulawesi Tengah}" -v id="SURGE-$(date -u +%s)" -v now="$(date -u +%s)" 'BEGIN {
        srand()
        for (i = 0; i < n; i++)
          printf "{\"event_id\":\"%s-%d\",\"region_name\":\"%s\",\"epicenter_lat\":%.3f,\"epicenter_lon\":%.3f,\"magnitude\":%.1f,\"depth_km\":%.0f,\"occurred_at\":\"%s\"}\n",
            id, i, region, -0.9 + rand() - 0.5, 119.8 + rand() - 0.5, 2.5 + rand() * 2.5, 5 + rand() * 30, strftime("%Y-%m-%dT%H:%M:%SZ", now - (n - i), 1)
      }' >"$work/events"
    fi
    mkdir -p "$ROOT/artifacts/m2/runs"
    log="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-$1.log"
    exec > >(tee "$log") 2>&1
    stats() {
      for pod in $(kubectl -n "$NS" get pods -l app=store -o name); do
        printf '%s|' "${pod#pod/}"
        kubectl -n "$NS" exec -i "$pod" -c postgres -- sh -c 'psql -qAt -U "$POSTGRES_USER" -d "$POSTGRES_DB"' <<'SQL' || echo "-|0|0"
SELECT coalesce(current_setting('bnpb.node_name', true), '?'), tup_inserted + tup_updated, (SELECT count(*) FROM hazard_events)
FROM pg_stat_database WHERE datname = current_database();
SQL
      done | sort
    }
    token="$(secret_value audit-token token)"
    holder="$(kubectl -n "$NS" get lease aggregator-leader -o jsonpath='{.spec.holderIdentity}')"
    [[ -n "$holder" ]] || die "Aggregator has no elected leader yet"
    forward "pod/${holder%%_*}" 18083:8083
    stats >"$work/before"
    (while :; do kubectl -n "$NS" top pod -l app=store --no-headers 2>/dev/null | sed "s/^/$(date -u +%T) top /"; sleep 5; done) &
    FORWARDS+=("$!")
    split -l "${CHUNK:-100}" "$work/events" "$work/chunk."
    echo "memuat $(wc -l <"$work/events") event dalam potongan ${CHUNK:-100}"
    started=$(date +%s)
    for chunk in "$work"/chunk.*; do
      curl_auth "$token" -fsS -X POST http://127.0.0.1:18083/internal/admin/load \
        -H "Content-Type: application/x-ndjson" --data-binary "@$chunk" -w " http_s=%{time_total}\n" \
        || die "potongan $chunk gagal; muat ulang aman (upsert idempoten per event_id)"
    done
    echo "selesai dalam $(($(date +%s) - started)) s"
    stats >"$work/after"
    echo "unit | node | tulis (tup_inserted+updated) selama run | rows sesudah"
    join -t'|' "$work/before" "$work/after" | awk -F'|' '{ printf "%s | %s | %d | %d\n", $1, $5, $6 - $3, $7 }'
    echo "log: $log"
    rm -rf "$work"
    ;;
  # Pull every workload image on each node before a demo, so a rescheduled pod does not wait
  # on Docker Hub. A privileged pod per node runs that node's own crictl through the host root.
  prepull)
    mapfile -t images < <(kubectl get deployment,statefulset,daemonset -A -o jsonpath='{range .items[*]}{range .spec.template.spec.initContainers[*]}{.image}{"\n"}{end}{range .spec.template.spec.containers[*]}{.image}{"\n"}{end}{end}' | sort -u)
    [[ ${#images[@]} -gt 0 ]] || die "no workload images found"
    image_args=""
    for image in "${images[@]}"; do
      [[ "$image" =~ ^[A-Za-z0-9./:@_-]+$ ]] || die "unexpected image reference: $image"
      image_args+=",\"$image\""
    done
    read -r -a nodes <<<"${2:-$(kubectl get nodes -o jsonpath='{.items[*].metadata.name}')}"
    for node in "${nodes[@]}"; do
      [[ "$node" =~ ^[a-z0-9.-]+$ ]] || die "invalid node name: $node"
      kubectl -n "$NS" delete pod "prepull-$node" --ignore-not-found --wait >/dev/null
      echo "+ prepull ${#images[@]} images on $node" >&2
      kubectl apply -f - >/dev/null <<EOF
{"apiVersion": "v1", "kind": "Pod",
 "metadata": {"name": "prepull-$node", "namespace": "$NS", "labels": {"app": "prepull"}},
 "spec": {"nodeName": "$node", "restartPolicy": "Never", "automountServiceAccountToken": false,
  "tolerations": [{"operator": "Exists"}],
  "containers": [{"name": "pull", "image": "busybox:1.36", "securityContext": {"privileged": true},
   "command": ["chroot", "/host", "sh", "-c", "failed=0; for image in \"\$@\"; do if k3s crictl pull \"\$image\" >/dev/null; then echo \"ok     \$image\"; else echo \"FAILED \$image\"; failed=1; fi; done; exit \$failed", "prepull"$image_args],
   "volumeMounts": [{"name": "host", "mountPath": "/host"}]}],
  "volumes": [{"name": "host", "hostPath": {"path": "/"}}]}}
EOF
    done
    failed=0
    for node in "${nodes[@]}"; do
      phase=""
      for _ in $(seq 1 180); do
        phase="$(kubectl -n "$NS" get pod "prepull-$node" -o jsonpath='{.status.phase}')"
        [[ "$phase" == Succeeded || "$phase" == Failed ]] && break
        sleep 5
      done
      echo "== $node ($phase)"
      kubectl -n "$NS" logs "prepull-$node" 2>/dev/null || true
      [[ "$phase" == Succeeded ]] || failed=1
      kubectl -n "$NS" delete pod "prepull-$node" --wait=false >/dev/null
    done
    [[ "$failed" == 0 ]] || die "some images were not pulled; see above"
    ;;
  *)
    die "unknown command '${1:-}'; see make help"
    ;;
esac
