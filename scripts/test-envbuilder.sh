#!/usr/bin/env bash
set -euo pipefail

# Smoke-tests that envbuilder can build the slaktforskning devcontainer
# and push the cached image to the local registry.
# Cold build: several minutes. Warm build: < 30s.

kubectl run envbuilder-test \
  --image=ghcr.io/coder/envbuilder:latest \
  --restart=Never \
  --env="ENVBUILDER_REPO_URL=https://github.com/jonaseck2/slaktforskning" \
  --env="ENVBUILDER_CACHE_REPO=slaktforskning-registry:5050/slaktforskning-devcontainer" \
  --env="ENVBUILDER_PUSH_IMAGE=true" \
  --overrides='{"spec":{"volumes":[{"name":"w","emptyDir":{}}],"containers":[{"name":"envbuilder-test","volumeMounts":[{"name":"w","mountPath":"/workspaces"}]}]}}'

echo "Following envbuilder-test logs (cold build: several minutes)..."
kubectl wait --for=condition=Ready pod/envbuilder-test --timeout=600s || true
kubectl logs envbuilder-test --follow || true

PHASE=$(kubectl get pod envbuilder-test -o jsonpath='{.status.phase}')
echo "Pod phase: ${PHASE}"
kubectl delete pod envbuilder-test --ignore-not-found

if [ "${PHASE}" = "Succeeded" ]; then
  echo "PASS: envbuilder build succeeded"
  curl -s "http://localhost:5050/v2/slaktforskning-devcontainer/tags/list"
  echo ""
else
  echo "FAIL: envbuilder build failed (phase=${PHASE})"
  exit 1
fi
