# POC Phase 4: Triage Agent, Packaging, Demo

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the loop. Issues labeled `needs-triage` get triaged automatically (implementation plan written, `ready-for-development` applied). Package the whole pipeline for single-command bring-up. Run the full demo end-to-end.

**Architecture:** Triage CronJob runs `claude -p` every 5 minutes against `needs-triage` issues. Kustomization installs everything. `make demo` exercises the full loop.

**Tech Stack:** Kubernetes CronJob, kustomize, Makefile, gh CLI

**Prerequisite:** Phase 3 complete. Full state machine works. Label an issue → PR appears → namespace cleaned up.

---

### Task 1: Implement the triage CronJob

**Files:**
- Create: `deploy/triage/cronjob.yaml`
- Create: `deploy/triage/configmap-prompt.yaml`
- Create: `deploy/triage/networkpolicy.yaml`
- Create: `deploy/triage/rbac.yaml`

The triage agent runs in its own namespace (`agentic-dev-pipeline-triage`), with narrower egress than implementation pods (no package registries, no repo clone — just GitHub API and Anthropic API).

- [ ] **Step 1: Create the triage namespace manifest**

```bash
mkdir -p /Users/jonasahnstedt/git/agentic-dev-pipeline/deploy/triage
```

Create `deploy/triage/namespace.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: agentic-dev-pipeline-triage
  labels:
    app.kubernetes.io/managed-by: agentic-dev-pipeline
```

- [ ] **Step 2: Create the triage prompt ConfigMap**

Create `deploy/triage/configmap-prompt.yaml`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: triage-prompt
  namespace: agentic-dev-pipeline-triage
data:
  prompt.txt: |
    You are a triage agent for the repository jonaseck2/slaktforskning.

    For GitHub issue #ISSUE_NUMBER:

    1. Read the issue body and all comments via the GitHub MCP (mcp__github).
    2. Read the repository structure to understand what files exist:
       - List the top-level directories and key source files
       - Read any README or DEVELOPING.md
    3. Decide: is this issue ready to implement as-is?

    If YES (the issue is specific, the required files exist, the approach is clear):
      - Write a detailed implementation plan as a comment on the issue.
        The plan must include:
        * Specific files to change with line references where relevant
        * The exact code change or approach (pseudocode if not certain)
        * How to run the tests: `npm test`
        * Acceptance criteria
      - Add label "ready-for-development"
      - Remove label "needs-triage"

    If NO (vague requirements, missing context, unclear scope):
      - Post a comment asking for the specific missing information.
        Be concrete: "What should the output format be?" not "Please clarify."
      - Add label "needs-info"
      - Remove label "needs-triage"

    Do not implement the change. Do not modify any source files.
    Only use the GitHub MCP. Do not use Bash, Read, Edit, or Write tools.
```

- [ ] **Step 3: Create the triage NetworkPolicy**

Create `deploy/triage/networkpolicy.yaml`:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: triage-egress
  namespace: agentic-dev-pipeline-triage
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
  ingress: []
  egress:
  - ports:
    - protocol: UDP
      port: 53
  - ports:
    - protocol: TCP
      port: 443
```

- [ ] **Step 4: Create the triage CronJob**

Create `deploy/triage/cronjob.yaml`:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: triage-agent
  namespace: agentic-dev-pipeline-triage
spec:
  schedule: "*/5 * * * *"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      activeDeadlineSeconds: 240
      template:
        spec:
          restartPolicy: Never
          serviceAccountName: triage-agent
          securityContext:
            runAsNonRoot: true
            seccompProfile:
              type: RuntimeDefault
          containers:
          - name: triage
            image: node:20-slim
            securityContext:
              allowPrivilegeEscalation: false
              readOnlyRootFilesystem: true
              capabilities:
                drop: [ALL]
            env:
            - name: GITHUB_TOKEN
              valueFrom:
                secretKeyRef:
                  name: pipeline-creds
                  key: github-token
            - name: ANTHROPIC_API_KEY
              valueFrom:
                secretKeyRef:
                  name: pipeline-creds
                  key: anthropic-api-key
            volumeMounts:
            - name: tmp
              mountPath: /tmp
            - name: npm-cache
              mountPath: /root/.npm
            - name: prompt
              mountPath: /config
              readOnly: true
            command:
            - bash
            - -c
            - |
              set -e
              # Install Claude Code CLI if not cached
              npm install -g @anthropic-ai/claude-code --cache /tmp/npm-cache 2>/dev/null

              # List open needs-triage issues
              ISSUES=$(npx --yes @octokit/cli issues list \
                --owner jonaseck2 --repo slaktforskning \
                --label needs-triage --state open \
                --json number --jq '.[].number' 2>/dev/null || \
                curl -s -H "Authorization: Bearer ${GITHUB_TOKEN}" \
                  "https://api.github.com/repos/jonaseck2/slaktforskning/issues?labels=needs-triage&state=open" \
                | node -e "const d=require('fs').readFileSync('/dev/stdin','utf8'); JSON.parse(d).forEach(i=>console.log(i.number))")

              for ISSUE_NUMBER in ${ISSUES}; do
                PROMPT=$(sed "s/ISSUE_NUMBER/${ISSUE_NUMBER}/g" /config/prompt.txt)
                claude -p "${PROMPT}" \
                  --allowedTools "mcp__github" \
                  --dangerously-skip-permissions \
                  --output-format json \
                  > /tmp/triage-${ISSUE_NUMBER}.json 2>&1 || true
                echo "Triaged issue #${ISSUE_NUMBER}"
              done
          volumes:
          - name: tmp
            emptyDir: {}
          - name: npm-cache
            emptyDir: {}
          - name: prompt
            configMap:
              name: triage-prompt
```

- [ ] **Step 5: Create RBAC**

Create `deploy/triage/rbac.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: triage-agent
  namespace: agentic-dev-pipeline-triage
---
# Triage agent needs no Kubernetes permissions; all work is via GitHub MCP
```

- [ ] **Step 6: Create the pipeline-creds Secret in triage namespace**

The triage pod also needs `pipeline-creds`. Either create a separate Secret or use a cross-namespace reference (we use a separate copy for isolation):

Add to `deploy/triage/secret-template.yaml`:

```yaml
# Run: kubectl create secret generic pipeline-creds \
#   --namespace agentic-dev-pipeline-triage \
#   --from-literal=github-token=<TOKEN> \
#   --from-literal=anthropic-api-key=<KEY>
#
# This file is intentionally empty — secrets are never committed.
apiVersion: v1
kind: Secret
metadata:
  name: pipeline-creds
  namespace: agentic-dev-pipeline-triage
type: Opaque
# data: set manually via kubectl create secret
```

- [ ] **Step 7: Test the triage CronJob manually**

```bash
kubectl apply -f /Users/jonasahnstedt/git/agentic-dev-pipeline/deploy/triage/

kubectl create secret generic pipeline-creds \
  --namespace agentic-dev-pipeline-triage \
  --from-literal=github-token="${GITHUB_TOKEN}" \
  --from-literal=anthropic-api-key="${ANTHROPIC_API_KEY}"

# Manually trigger the CronJob
kubectl create job --from=cronjob/triage-agent triage-test \
  -n agentic-dev-pipeline-triage

kubectl logs -n agentic-dev-pipeline-triage job/triage-test --follow
```

- [ ] **Step 8: File two test issues**

```bash
# Issue 1: well-formed
gh issue create --repo jonaseck2/slaktforskning \
  --title "Add export to CSV for person search results" \
  --label "needs-triage" \
  --body "When searching for persons, add a button or CLI flag to export results as CSV with columns: id, name, birth_year, death_year, birth_place."

# Issue 2: intentionally vague
gh issue create --repo jonaseck2/slaktforskning \
  --title "Improve search" \
  --label "needs-triage" \
  --body "Search could be better. Maybe add more filters or something."
```

Wait for the CronJob to fire (up to 5 minutes) or trigger it manually. Verify:

- Issue 1: gets a concrete implementation plan + `ready-for-development` label
- Issue 2: gets a clarifying question + `needs-info` label

- [ ] **Step 9: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add deploy/triage/
git commit -m "feat: triage CronJob runs claude -p every 5 minutes against needs-triage issues"
```

---

### Task 2: Create the kustomization for single-command install

**Files:**
- Create: `deploy/kustomization.yaml`
- Create: `deploy/system/namespace.yaml`
- Create: `deploy/system/kustomization.yaml`

- [ ] **Step 1: Create system namespace manifest**

```bash
mkdir -p /Users/jonasahnstedt/git/agentic-dev-pipeline/deploy/system
```

Create `deploy/system/namespace.yaml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: devpipeline-system
  labels:
    app.kubernetes.io/managed-by: agentic-dev-pipeline
```

- [ ] **Step 2: Create root kustomization.yaml**

Create `deploy/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - system/namespace.yaml
  - triage/namespace.yaml
  - triage/networkpolicy.yaml
  - triage/rbac.yaml
  - triage/configmap-prompt.yaml
  - triage/cronjob.yaml
  # CRD and operator added after `make install` in the operator directory
```

- [ ] **Step 3: Test kustomize apply**

```bash
kubectl apply -k /Users/jonasahnstedt/git/agentic-dev-pipeline/deploy/
```

Expected: All resources created without errors.

- [ ] **Step 4: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add deploy/
git commit -m "feat: kustomization for single-command pipeline install"
```

---

### Task 3: Write the Makefile and README

**Files:**
- Create: `Makefile`
- Create: `README.md`

- [ ] **Step 1: Create Makefile**

```bash
cat > /Users/jonasahnstedt/git/agentic-dev-pipeline/Makefile << 'MAKEFILE'
.PHONY: cluster install secrets demo clean

# Create k3d cluster with Calico
cluster:
	./scripts/cluster-create.sh

# Install CRDs and pipeline components
install:
	cd operator && make install
	kubectl apply -k deploy/

# Create secrets (requires GITHUB_TOKEN and ANTHROPIC_API_KEY env vars)
secrets:
	@test -n "$(GITHUB_TOKEN)" || (echo "GITHUB_TOKEN not set" && exit 1)
	@test -n "$(ANTHROPIC_API_KEY)" || (echo "ANTHROPIC_API_KEY not set" && exit 1)
	kubectl create secret generic pipeline-creds \
		--namespace devpipeline-system \
		--from-literal=github-token="$(GITHUB_TOKEN)" \
		--from-literal=anthropic-api-key="$(ANTHROPIC_API_KEY)" \
		--dry-run=client -o yaml | kubectl apply -f -
	kubectl create secret generic pipeline-creds \
		--namespace agentic-dev-pipeline-triage \
		--from-literal=github-token="$(GITHUB_TOKEN)" \
		--from-literal=anthropic-api-key="$(ANTHROPIC_API_KEY)" \
		--dry-run=client -o yaml | kubectl apply -f -

# Run the operator locally (for development)
run:
	cd operator && make run

# Full demo: file an issue, watch it go through triage + implementation
demo:
	@echo "Filing a demo issue on jonaseck2/slaktforskning..."
	@ISSUE_NUMBER=$$(gh issue create \
		--repo jonaseck2/slaktforskning \
		--title "Demo: add birth-year filter to person search" \
		--label "needs-triage" \
		--body "Add a --birth-year-min and --birth-year-max flag to the search_persons MCP tool. Filter results to persons born within the given range. If no range given, return all results as today." \
		| grep -oE '[0-9]+$$') && \
	echo "Issue #$${ISSUE_NUMBER} filed. Waiting for triage (up to 5 minutes)..." && \
	echo "Watch: gh issue view $${ISSUE_NUMBER} --repo jonaseck2/slaktforskning" && \
	echo "Watch: kubectl get devtask -n devpipeline-system --watch"

# Tear down the cluster
clean:
	k3d cluster delete slaktforskning-poc
MAKEFILE
```

- [ ] **Step 2: Create README.md**

Create `/Users/jonasahnstedt/git/agentic-dev-pipeline/README.md`:

```markdown
# Agentic Development Pipeline — POC

Claude as maintainer of [`slaktforskning`](https://github.com/jonaseck2/slaktforskning), running on a local k3d cluster.

## Bring-up (one-time)

**Prerequisites:** `brew install k3d kubectl kubebuilder helm go gh`

```bash
# 1. Create cluster with Calico
make cluster

# 2. Install CRDs and pipeline components
make install

# 3. Set credentials
export GITHUB_TOKEN=<fine-grained-PAT-for-slaktforskning>
export ANTHROPIC_API_KEY=<your-key>
make secrets
```

The fine-grained PAT needs: Contents Read+Write, Issues Read+Write, Pull Requests Read+Write.

## Running the operator

```bash
make run
```

Runs the operator locally against the cluster. Leave this terminal open.

## Demo

```bash
make demo
```

Files a real issue → triage agent writes an implementation plan → implementation agent opens a PR.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md).

## Plans

See [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md).
```

- [ ] **Step 3: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add Makefile README.md
git commit -m "feat: Makefile + README for single-command bring-up"
```

---

### Task 4: Run the full end-to-end demo

This task validates that everything works together. Follow these steps exactly.

- [ ] **Step 1: Fresh cluster bring-up**

```bash
k3d cluster delete slaktforskning-poc 2>/dev/null || true
make cluster
make install
make secrets
```

- [ ] **Step 2: Start the operator**

In a separate terminal:

```bash
make run
```

- [ ] **Step 3: File a real issue with `needs-triage`**

```bash
gh issue create \
  --repo jonaseck2/slaktforskning \
  --title "Add birth-year range filter to search_persons" \
  --label "needs-triage" \
  --body "The search_persons MCP tool should accept optional birth_year_min and birth_year_max parameters. When provided, only return persons whose birth year is within the given range. When omitted, return all results as today."
```

Note the issue number.

- [ ] **Step 4: Watch triage run**

```bash
# Trigger triage manually or wait up to 5 minutes
kubectl create job --from=cronjob/triage-agent triage-demo \
  -n agentic-dev-pipeline-triage

kubectl logs -n agentic-dev-pipeline-triage job/triage-demo --follow
```

Expected: Issue gets an implementation plan comment + `ready-for-development` label.

- [ ] **Step 5: Watch DevTask get created**

After the label is applied, the operator should detect it within 30 seconds:

```bash
kubectl get devtask -n devpipeline-system --watch
```

Expected: `slaktforskning-<NUMBER>` appears, progresses: `Building` → `Running` → `AwaitingReview`

- [ ] **Step 6: Watch the agent pod**

```bash
NS=devtask-<ISSUE-NUMBER>
kubectl logs -n "${NS}" agent --follow
```

Expected: claude -p output showing it reading the issue, making changes, running tests, opening a PR.

- [ ] **Step 7: Verify the PR**

```bash
gh pr list --repo jonaseck2/slaktforskning --state open
```

Open the PR in browser, review the diff, check CI status.

- [ ] **Step 8: Merge the PR and watch cleanup**

```bash
gh pr merge <PR-NUMBER> --repo jonaseck2/slaktforskning --squash
```

Within 2 minutes:

```bash
kubectl get devtask slaktforskning-<ISSUE> -n devpipeline-system
```

Expected: Phase `Completed`.

```bash
kubectl get namespace devtask-<ISSUE>
```

Expected: `NotFound` — namespace deleted.

- [ ] **Step 9: Record results**

Write a brief note in `docs/plans/archive/poc-demo-results.md`:

```markdown
# POC Demo Results

Date: 2026-04-22

## Issue: Add birth-year range filter to search_persons
- Issue filed: #<N>
- Triage time: <X> minutes
- Implementation time: <X> minutes
- PR quality: <notes>
- Tests passed: Y/N
- Total cost: $<X.XX> (from DevTask.status.costUSD)

## What worked
- ...

## What needed adjustment
- ...
```

---

## Exit Criteria

Phase 4 is complete when:

1. `make demo` reliably produces a real PR from a real issue with no manual intervention between filing and reviewing
2. `make cluster && make install && make secrets && make run` brings up the whole pipeline on a fresh cluster
3. A vague issue gets a `needs-info` label and a clarifying question from the triage agent
4. A well-formed issue gets a `ready-for-development` label and a concrete implementation plan
5. Demo results documented in `docs/plans/archive/poc-demo-results.md`

Move this plan to `docs/plans/archive/` when done.
