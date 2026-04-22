#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME=slaktforskning-poc
REGISTRY_NAME=slaktforskning-registry
REGISTRY_PORT=5050

echo "Creating k3d cluster: ${CLUSTER_NAME}"
k3d cluster create "${CLUSTER_NAME}" \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create "${REGISTRY_NAME}:${REGISTRY_PORT}" \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

kubectl wait --for=condition=Ready nodes --all --timeout=120s

echo "Installing Calico CNI..."
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml

kubectl wait --for=condition=Ready nodes --all --timeout=180s

echo "Cluster ready. Registry at localhost:${REGISTRY_PORT}"
echo "To create the system namespace and install CRDs:"
echo "  kubectl create namespace devpipeline-system --dry-run=client -o yaml | kubectl apply -f -"
echo "  cd operator && make install"
