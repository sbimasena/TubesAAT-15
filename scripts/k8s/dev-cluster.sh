#!/usr/bin/env bash
# Create (or start) the local k3d development cluster: 1 server + 2 agents = 3 nodes,
# the same shape as the demo cluster (D-TOPO) and the same k3s version.
# Client API is published on http://127.0.0.1:${HOST_PORT} through NodePort 30080.
set -euo pipefail

CLUSTER="${CLUSTER:-bnpb-dev}"
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.33.5-k3s1}"
AGENTS="${AGENTS:-2}"
HOST_PORT="${HOST_PORT:-8080}"

for tool in docker k3d kubectl; do
  command -v "$tool" >/dev/null || { echo "missing tool: $tool" >&2; exit 1; }
done

if k3d cluster list "$CLUSTER" >/dev/null 2>&1; then
  echo "+ k3d cluster start $CLUSTER"
  k3d cluster start "$CLUSTER"
else
  echo "+ k3d cluster create $CLUSTER ($AGENTS agents, $K3S_IMAGE)"
  # Keep NetworkPolicy enforcement on (kube-router), as on the demo cluster.
  k3d cluster create "$CLUSTER" \
    --image "$K3S_IMAGE" \
    --agents "$AGENTS" \
    --port "${HOST_PORT}:30080@loadbalancer" \
    --wait --timeout 180s
fi

kubectl config use-context "k3d-$CLUSTER" >/dev/null
kubectl wait --for=condition=Ready nodes --all --timeout=120s
kubectl get nodes -o wide
