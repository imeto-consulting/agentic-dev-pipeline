#!/usr/bin/env bash
# Verify the runtime security controls that unit tests cannot prove — the ones
# that depend on the live cluster actually enforcing what the manifests declare
# (Calico enforcing NetworkPolicy, the App-token rotation reaching the triage
# Secret, the operator's RBAC as applied). Run against a bootstrapped cluster.
#
# Usage: scripts/verify-hardening.sh
#
# Reproducible replacement for the manual "demo verification" checklist. Each
# check prints PASS/FAIL/SKIP; the script exits non-zero if any check FAILs.
#
# NOT covered here (covered by `make -C operator test` unit tests, or by the
# documented manual checklist in docs/plans/...phase5...md): the diff-policy
# rejection, the plan-review label gate, and the author-association resume gate
# all depend on a live agent run, which is non-deterministic to script.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck disable=SC1091
[ -f "${ROOT_DIR}/.pipeline.env" ] && source "${ROOT_DIR}/.pipeline.env"

SYS_NS="devpipeline-system"
TRIAGE_NS="${TRIAGE_NAMESPACE:-agentic-dev-pipeline-triage}"
PROXY_NS="${EGRESS_PROXY_NAMESPACE:-egress-proxy}"
PROBE_NS="hardening-probe"

fails=0
pass()  { printf '  \033[32mPASS\033[0m  %s\n' "$1"; }
fail()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; fails=$((fails+1)); }
skip()  { printf '  \033[33mSKIP\033[0m  %s\n' "$1"; }
hdr()   { printf '\n== %s ==\n' "$1"; }

cleanup() { kubectl delete ns "${PROBE_NS}" --ignore-not-found --wait=false >/dev/null 2>&1 || true; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
hdr "Operator RBAC (as applied)"
# The triage-token refresher needs secrets update; verify the live ClusterRole.
if kubectl get clusterrole manager-role -o jsonpath='{range .rules[?(@.resources[0]=="secrets")]}{.verbs}{end}' 2>/dev/null | grep -q update \
   || kubectl get clusterrole manager-role -o json 2>/dev/null | grep -q '"update"'; then
  pass "manager-role can update secrets (triage token rotation)"
else
  fail "manager-role missing secrets:update — triage token rotation will be denied"
fi

# ---------------------------------------------------------------------------
hdr "GitHub App token rotation (triage)"
if kubectl get secret pipeline-app-key -n "${SYS_NS}" >/dev/null 2>&1; then
  TOK="$(kubectl get secret pipeline-creds -n "${TRIAGE_NS}" -o jsonpath='{.data.github-token}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  if [ -z "${TOK}" ]; then
    fail "triage pipeline-creds has no github-token (run make secrets, then let the operator run once)"
  elif printf '%s' "${TOK}" | grep -q '^ghs_'; then
    pass "triage github-token is a GitHub App installation token (ghs_…)"
  else
    fail "App mode is on but triage github-token is not an installation token — refresher hasn't run yet?"
  fi
else
  skip "PAT mode (no pipeline-app-key) — triage uses the PAT by design"
fi

# ---------------------------------------------------------------------------
hdr "Egress enforcement (Calico)"
# Mirror the sandbox: a managed-by-labeled namespace + the operator's egress
# policy shape, then probe from inside it. This proves the CNI enforces the
# policy, which the operator unit tests (policy-object shape) cannot.
kubectl create ns "${PROBE_NS}" >/dev/null 2>&1 || true
kubectl label ns "${PROBE_NS}" app.kubernetes.io/managed-by=agentic-dev-pipeline --overwrite >/dev/null 2>&1 || true

if kubectl get ns "${PROXY_NS}" >/dev/null 2>&1 && [ -n "${EGRESS_PROXY_URL:-}" ]; then
  MODE="proxy"
  PROXY_PORT="${EGRESS_PROXY_PORT:-3128}"
  cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: sandbox-egress, namespace: ${PROBE_NS} }
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
  ingress: []
  egress:
  - ports: [{protocol: UDP, port: 53}, {protocol: TCP, port: 53}]
  - to: [{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: ${PROXY_NS}}}}]
    ports: [{protocol: TCP, port: ${PROXY_PORT}}]
YAML
else
  MODE="default"
  cat <<YAML | kubectl apply -f - >/dev/null
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: sandbox-egress, namespace: ${PROBE_NS} }
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
  ingress: []
  egress:
  - ports: [{protocol: UDP, port: 53}, {protocol: TCP, port: 53}]
  - ports: [{protocol: TCP, port: 443}]
YAML
fi
echo "  (egress mode: ${MODE})"

probe() { # probe <url> [proxy-url]
  local url="$1" proxy="${2:-}" envs=()
  [ -n "${proxy}" ] && envs=(--env=HTTPS_PROXY="${proxy}" --env=HTTP_PROXY="${proxy}")
  kubectl run egress-probe -n "${PROBE_NS}" --rm -i --restart=Never --image=curlimages/curl:8.10.1 \
    "${envs[@]}" --command -- \
    curl -sS --max-time 12 -o /dev/null -w '%{http_code}' "${url}" 2>/dev/null
}

if [ "${MODE}" = "proxy" ]; then
  ALLOW="$(probe https://api.github.com "${EGRESS_PROXY_URL}")"
  [ -n "${ALLOW}" ] && [ "${ALLOW}" != "000" ] && pass "allowlisted host reachable via proxy (HTTP ${ALLOW})" \
    || fail "allowlisted host NOT reachable via proxy (got '${ALLOW}') — check the Squid allowlist"
  DENY="$(probe https://example.com "${EGRESS_PROXY_URL}")"
  if [ -z "${DENY}" ] || [ "${DENY}" = "000" ] || [ "${DENY}" = "403" ]; then
    pass "non-allowlisted host blocked by proxy (got '${DENY:-blocked}')"
  else
    fail "non-allowlisted host REACHABLE (HTTP ${DENY}) — egress allowlist not enforced"
  fi
  DIRECT="$(probe https://example.com)"
  [ -z "${DIRECT}" ] || [ "${DIRECT}" = "000" ] \
    && pass "direct :443 (bypassing proxy) blocked by NetworkPolicy" \
    || fail "direct :443 reachable (HTTP ${DIRECT}) — NetworkPolicy not enforced by the CNI (Calico installed?)"
else
  REACH="$(probe https://api.github.com)"
  [ -n "${REACH}" ] && [ "${REACH}" != "000" ] \
    && pass "sandbox can reach :443 (HTTP ${REACH}); host-scoping is intentionally OFF in default mode" \
    || fail "sandbox cannot reach :443 (got '${REACH}') — DNS or egress policy misconfigured"
  echo "  note: default mode allows any :443 host by design. For host-scoped egress,"
  echo "        deploy deploy/egress-proxy/ and set EGRESS_PROXY_URL (see README)."
fi

# ---------------------------------------------------------------------------
hdr "Egress proxy health (if deployed)"
if kubectl get ns "${PROXY_NS}" >/dev/null 2>&1; then
  if kubectl wait -n "${PROXY_NS}" --for=condition=available deploy/egress-proxy --timeout=30s >/dev/null 2>&1; then
    pass "egress-proxy deployment is available"
  else
    fail "egress-proxy deployment not available"
  fi
else
  skip "egress-proxy not deployed"
fi

# ---------------------------------------------------------------------------
printf '\n'
if [ "${fails}" -eq 0 ]; then
  printf '\033[32mAll hardening checks passed.\033[0m\n'
  exit 0
fi
printf '\033[31m%d hardening check(s) failed.\033[0m\n' "${fails}"
exit 1
