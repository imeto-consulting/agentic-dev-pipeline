#!/usr/bin/env bash
set -euo pipefail

# Smoke-tests that envbuilder can build the slaktforskning devcontainer
# and push the cached image to the local registry.
# Cold build: several minutes. Warm build: < 30s.
# Requires GITHUB_PERSONAL_ACCESS_TOKEN to clone the private repo.

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
      value: https://github.com/jonaseck2/slaktforskning
    - name: ENVBUILDER_GIT_USERNAME
      value: x-access-token
    - name: ENVBUILDER_GIT_PASSWORD
      value: "${GITHUB_PERSONAL_ACCESS_TOKEN}"
    - name: ENVBUILDER_CACHE_REPO
      value: slaktforskning-registry:5000/slaktforskning-devcontainer
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
  curl -s "http://localhost:5050/v2/slaktforskning-devcontainer/tags/list"
  echo ""
else
  echo "FAIL: envbuilder build failed (phase=${PHASE})"
  exit 1
fi
