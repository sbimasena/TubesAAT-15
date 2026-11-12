#!/usr/bin/env bash
# Create (or start) the local k3d development cluster: 1 server + 2 agents = 3 nodes,
# the same shape as the demo cluster (D-TOPO) and the same k3s version.
# Client API is published on http://127.0.0.1:${HOST_PORT} through NodePort 30080.
#   dev-cluster.sh          create or start
#   dev-cluster.sh delete   delete the cluster and all its data
source "$(dirname "$0")/lib.sh"

CLUSTER="${CLUSTER:-bnpb-dev}"
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.33.5-k3s1}"
AGENTS="${AGENTS:-2}"
HOST_PORT="${HOST_PORT:-8080}"

need docker k3d kubectl

if [[ "${1:-}" == delete ]]; then
  confirm "Delete k3d cluster '$CLUSTER' and all its data?"
  run k3d cluster delete "$CLUSTER"
  exit 0
fi

if k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
  run k3d cluster start "$CLUSTER"
else
  # Keep NetworkPolicy enforcement on (kube-router), as on the demo cluster.
  run k3d cluster create "$CLUSTER" \
    --image "$K3S_IMAGE" \
    --agents "$AGENTS" \
    --port "${HOST_PORT}:30080@loadbalancer" \
    --wait --timeout 180s
fi

run kubectl config use-context "k3d-$CLUSTER"
run kubectl wait --for=condition=Ready nodes --all --timeout=120s
run kubectl get nodes -o wide
