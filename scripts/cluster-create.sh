#!/usr/bin/env bash
set -euo pipefail

# Source pipeline config from project root.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "${SCRIPT_DIR}")"

if [ ! -f "${ROOT_DIR}/.pipeline.env" ]; then
  echo "Error: ${ROOT_DIR}/.pipeline.env not found. Run 'make init' first." >&2
  exit 1
fi
# shellcheck source=/dev/null
. "${ROOT_DIR}/.pipeline.env"

: "${CLUSTER_NAME:?CLUSTER_NAME not set in .pipeline.env}"
: "${REGISTRY_NAME:?REGISTRY_NAME not set in .pipeline.env}"
REGISTRY_PORT=5050

echo "Creating k3d cluster: ${CLUSTER_NAME}"
k3d cluster create "${CLUSTER_NAME}" \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create "${REGISTRY_NAME}:${REGISTRY_PORT}" \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

echo "Installing Calico CNI (nodes will be NotReady until this is up)..."
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml

echo "Waiting for nodes to become Ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=300s

echo "Cluster ready. Registry at localhost:${REGISTRY_PORT}"
echo "Next: make seed-image && make secrets && make install && make run"
