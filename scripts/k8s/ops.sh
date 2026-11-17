#!/usr/bin/env bash
# Day-to-day cluster operations behind the Makefile: ops.sh <command> [arg].
source "$(dirname "$0")/lib.sh"
need kubectl

FORWARDS=()
cleanup() {
  for pid in "${FORWARDS[@]}"; do kill "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

# forward runs a background port-forward and waits until the local port answers.
forward() {
  local target="$1" ports="$2"
  echo "+ kubectl -n $NS port-forward $target $ports" >&2
  kubectl -n "$NS" port-forward "$target" "$ports" >/dev/null &
  FORWARDS+=("$!")
  for _ in $(seq 50); do
    (exec 3<>"/dev/tcp/127.0.0.1/${ports%%:*}") 2>/dev/null && return 0
    sleep 0.2
  done
  die "port-forward to $target did not become ready"
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
      curl -fsS -X POST "http://127.0.0.1:18082/admin/${call%% *}" \
        -H "Authorization: Bearer $token" -H "Content-Type: application/json" -d "${call#* }"
      echo
    done
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
  p1-checksum)
    # Audit API checksum per source and over all sources (kontrak-m2.md §7.3).
    token="$(secret_value audit-token token)"
    forward svc/aggregator 18083:8083
    for source in BMKG PVMBG SATELIT SENSOR_PANTAI ""; do
      echo "+ curl http://127.0.0.1:18083/internal/audit/checksum?source=$source" >&2
      curl -sS "http://127.0.0.1:18083/internal/audit/checksum?source=$source" -H "Authorization: Bearer $token"
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
TRUNCATE hazard_events, hazard_outbox;
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
    page="$(curl -fsS "http://127.0.0.1:18083/internal/audit/events?source=BMKG&limit=2" -H "Authorization: Bearer $token")"
    echo "events: $page"
    id="$(grep -o '"hazard_id":"[^"]*"' <<<"$page" | head -1 | cut -d'"' -f4)"
    [[ -n "$id" ]] || die "tidak ada event BMKG untuk dicari lokasinya"
    echo "location $id: $(curl -fsS "http://127.0.0.1:18083/internal/audit/location?hazard_id=$id" -H "Authorization: Bearer $token")"
    echo "harapan: 2 lokasi (primary + replica) di node berbeda"
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
      curl -fsS -X POST http://127.0.0.1:18083/internal/admin/load -H "Authorization: Bearer $token" \
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
  *)
    die "unknown command '${1:-}'; see make help"
    ;;
esac
