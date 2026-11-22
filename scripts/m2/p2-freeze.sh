#!/usr/bin/env bash
# P2 freeze: suspend the VM that holds the Aggregator lease for longer than the lease
# (15 s), resume it, and collect evidence that the woken leader stops or is fenced.
# Runs on the VirtualBox host of that VM (node-a and node-c live on the PC host).
source "$(dirname "$0")/../k8s/lib.sh"
need kubectl
VBOX="${VBOX:-VBoxManage}"
command -v "$VBOX" >/dev/null || VBOX="/c/Program Files/Oracle/VirtualBox/VBoxManage.exe"
[[ -x "$VBOX" ]] || command -v "$VBOX" >/dev/null || die "VBoxManage not found; run on the VirtualBox host or set VBOX"
SECONDS_FROZEN="${FREEZE_SECONDS:-25}"
holder="$(kubectl -n "$NS" get lease aggregator-leader -o jsonpath='{.spec.holderIdentity}')"
pod="${holder%%_*}"
node="$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')"
"$VBOX" list runningvms | grep -q "\"$node\"" || die "leader $pod runs on $node, which is not a VM on this host"
confirm "Pause VM $node (leader $pod) for ${SECONDS_FROZEN}s in $(kubectl config current-context)?"
mkdir -p "$ROOT/artifacts/m2/runs"
out="$ROOT/artifacts/m2/runs/$(date -u +%Y%m%dT%H%M%SZ)-p2-freeze.log"
exec > >(tee "$out") 2>&1
kubectl -n "$NS" get lease aggregator-leader -o jsonpath='before: holder={.spec.holderIdentity} transitions={.spec.leaseTransitions} renew={.spec.renewTime}{"\n"}'
paused="$(date -u +%FT%TZ)"
echo "+ pause $node at $paused"
"$VBOX" controlvm "$node" pause
trap '"$VBOX" controlvm "$node" resume 2>/dev/null || true' EXIT
sleep "$SECONDS_FROZEN"
"$VBOX" controlvm "$node" resume
trap - EXIT
echo "+ resume $node at $(date -u +%FT%TZ)"
kubectl -n "$NS" get lease aggregator-leader -o jsonpath='after:  holder={.spec.holderIdentity} transitions={.spec.leaseTransitions} renew={.spec.renewTime}{"\n"}'
sleep 20
echo "--- woken former leader $pod (lease loss, worker stop, fencing)"
# The woken leader stops its workers and exits (fail-stop), so its evidence is in the
# previous container. The VM clock was frozen, so its timestamps lag wall time.
kubectl -n "$NS" get pod "$pod" -o jsonpath='restarts={.status.containerStatuses[0].restartCount} last={.status.containerStatuses[0].lastState.terminated.reason}{"\n"}' || true
{ kubectl -n "$NS" logs "$pod" --previous 2>/dev/null || kubectl -n "$NS" logs "$pod"; } |
  grep -iE "lease|leader|fenc|epoch|stopp" | tail -10 || true
new="$(kubectl -n "$NS" get lease aggregator-leader -o jsonpath='{.spec.holderIdentity}')"
echo "--- new leader ${new%%_*}"
kubectl -n "$NS" logs "${new%%_*}" --since-time="$paused" | grep -iE "leader workers|epoch|fence" | head -10 || true
echo "Evidence: $out. Check Audit checksums for partial:false and no duplicate hazard_id."
