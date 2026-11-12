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
    ours="$(sed -n 's#.*name: bnpb/aggregator, newName: \(.*\), newTag: \(.*\)}#\1:\2#p' "$GENERATED/kustomization.yaml")"
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
  *)
    die "unknown command '${1:-}'; see make help"
    ;;
esac
