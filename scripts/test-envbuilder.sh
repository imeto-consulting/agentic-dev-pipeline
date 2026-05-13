#!/usr/bin/env bash
set -euo pipefail

# Smoke-tests that envbuilder can build the target repo's devcontainer
# and push the cached image to the local registry.
# Cold build: several minutes. Warm build: < 30s.
# Requires GITHUB_PERSONAL_ACCESS_TOKEN to clone a private repo.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "${SCRIPT_DIR}")"

if [ ! -f "${ROOT_DIR}/.pipeline.env" ]; then
  echo "Error: ${ROOT_DIR}/.pipeline.env not found. Run 'make init' first." >&2
  exit 1
fi
# shellcheck source=/dev/null
. "${ROOT_DIR}/.pipeline.env"

: "${TARGET_REPO:?TARGET_REPO not set in .pipeline.env}"
: "${REGISTRY_NAME:?REGISTRY_NAME not set in .pipeline.env}"
: "${DEVCONTAINER_IMAGE:?DEVCONTAINER_IMAGE not set in .pipeline.env}"

kubectl apply -f - <<PODSPEC
apiVersion: v1
kind: Pod
metadata:
  name: envbuilder-test
  namespace: default
spec:
  restartPolicy: Never
  volumes:
  - name: workspaces
    emptyDir: {}
  containers:
  - name: envbuilder
    image: ghcr.io/coder/envbuilder:latest
    env:
    - name: ENVBUILDER_GIT_URL
      value: https://github.com/${TARGET_REPO}
    - name: ENVBUILDER_GIT_USERNAME
      value: x-access-token
    - name: ENVBUILDER_GIT_PASSWORD
      value: "${GITHUB_PERSONAL_ACCESS_TOKEN:-}"
    - name: ENVBUILDER_CACHE_REPO
      value: ${REGISTRY_NAME}:5000/${DEVCONTAINER_IMAGE}
    - name: ENVBUILDER_PUSH_IMAGE
      value: "true"
    - name: ENVBUILDER_INIT_SCRIPT
      value: "echo 'envbuilder smoke test complete'"
    - name: ENVBUILDER_INSECURE
      value: "true"
    volumeMounts:
    - name: workspaces
      mountPath: /workspaces
PODSPEC

echo "Following envbuilder-test logs (cold build: several minutes)..."
until kubectl get pod envbuilder-test -o jsonpath='{.status.phase}' 2>/dev/null | grep -qE 'Succeeded|Failed'; do
  sleep 5
done

PHASE=$(kubectl get pod envbuilder-test -o jsonpath='{.status.phase}')
kubectl logs envbuilder-test --tail=20
echo "Pod phase: ${PHASE}"
kubectl delete pod envbuilder-test --ignore-not-found

if [ "${PHASE}" = "Succeeded" ]; then
  echo "PASS: envbuilder build succeeded"
  curl -s "http://localhost:5050/v2/${DEVCONTAINER_IMAGE}/tags/list"
  echo ""
else
  echo "FAIL: envbuilder build failed (phase=${PHASE})"
  exit 1
fi
