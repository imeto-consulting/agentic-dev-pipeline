# POC: Claude as Maintainer of `jonaseck2/slaktforskning`

End-to-end agentic development pipeline, runnable on a laptop via k3d, targeting a real personal open-source repo. The goal is not "look, it works on a toy" — it's to have Claude genuinely handle triaged tickets against `slaktforskning`, with the full pipeline running locally, so the architecture gets exercised by real work.

## Scope

**In scope.**

- A k3d cluster running on the laptop
- A `DevTask` CRD and a small operator
- A namespace-per-task sandbox with egress controls
- envbuilder building the `slaktforskning` devcontainer
- Claude Code in headless mode as the agent
- A ticketing MCP server (GitHub Issues for this POC)
- A triage agent that produces implementation plans in the ticket
- The `ai-pipeline-enabled` topic on the repo as the opt-in

**Out of scope for the POC.**

- The skills-update-after-implementation loop (v2)
- Gating on CODEOWNERS (relying on personal repo conventions)
- L7 egress proxy (NetworkPolicy is enough at laptop scale)
- gVisor runtime (laptop k3d doesn't run it by default)
- Multi-repo support (single repo hardcoded initially)
- High availability, metrics, observability beyond `kubectl logs`

These get added once the happy path works end-to-end.

## What "working" looks like

1. I file a GitHub issue on `slaktforskning` describing a bug or small feature
2. Triage agent (cron or webhook) picks it up, enriches it, writes an implementation plan as an issue comment, labels it `ready-for-development`
3. Operator notices the label, creates a `DevTask` CR, creates a namespace, spins up a Coder workspace via envbuilder
4. The pod starts, `claude -p "Implement issue #42..."` runs, reads the plan via the GitHub MCP, does the work, opens a PR, comments back on the issue
5. I review the PR, merge or request changes
6. Operator observes the PR state, cleans up the namespace, transitions `DevTask` to `Completed`

End-to-end on my laptop, against a real repo, with a real issue.

## Architecture (POC version)

```
           GitHub Issues                           (actual github.com)
               │
               │  webhook via smee.io / polling
               ▼
┌──────────────────────────────────────┐
│  k3d cluster (laptop)                │
│                                      │
│  ┌────────────┐   ┌────────────────┐ │
│  │  triage    │   │  operator      │ │
│  │  CronJob   │   │  (kubebuilder) │ │
│  │  (every    │   │                │ │
│  │   5 min)   │   │   watches:     │ │
│  └─────┬──────┘   │    DevTask CR  │ │
│        │          └───────┬────────┘ │
│        │ labels           │          │
│        │ "ready..."       │ creates  │
│        ▼                  ▼          │
│  ┌──────────────────────────────┐    │
│  │  GitHub (external)           │    │
│  │  - issues, labels, comments  │    │
│  │  - PRs                       │    │
│  │  - the repo itself           │    │
│  └──────────────────────────────┘    │
│                  ▲                   │
│                  │ MCP + git push    │
│                  │                   │
│  ┌───────────────┴──────────────┐    │
│  │  namespace: devtask-42       │    │
│  │  ┌────────────────────────┐  │    │
│  │  │ envbuilder pod         │  │    │
│  │  │  .devcontainer/        │  │    │
│  │  │  → image built         │  │    │
│  │  │  → claude -p "..." runs│  │    │
│  │  └────────────────────────┘  │    │
│  │  NetworkPolicy: deny-all     │    │
│  │    + api.github.com          │    │
│  │    + api.anthropic.com       │    │
│  │    + registry.npmjs.org etc  │    │
│  └──────────────────────────────┘    │
└──────────────────────────────────────┘
```

Coder-the-platform is overkill for this POC — envbuilder alone does the container build, and a plain `Job` or `Pod` is enough to run the agent. We can add Coder later if we want its workspace UI and session management.

## Component design

### 1. k3d cluster

Single command to bring up, single command to tear down. Everything else lives in the cluster.

```bash
k3d cluster create slaktforskning-poc \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create slaktforskning-registry:5000
```

The local registry is important: envbuilder's layer cache will push here, which makes rebuilds fast. Without it every task rebuilds from scratch and laptop tolerance evaporates.

### 2. The `DevTask` CRD

Minimal shape for POC. Ticket reference, repo reference, status. No agent config (there's only one), no timeout override (use a sensible default).

```yaml
apiVersion: devpipeline.local/v1alpha1
kind: DevTask
metadata:
  name: slaktforskning-42
  namespace: devpipeline-system
spec:
  issueNumber: 42
  repo: jonaseck2/slaktforskning
status:
  phase: Pending | Building | Running | AwaitingReview | BlockedOnClarification | Failed | Completed
  namespace: devtask-42
  prNumber: 87
  startedAt: "2026-04-22T10:15:00Z"
  message: "..."
```

The CR is derived state — the operator creates it when the issue hits `ready-for-development`, deletes it (and the namespace) when the PR merges or the task fails permanently.

### 3. The operator

Kubebuilder scaffold, one controller. Reconcile loop:

```
func reconcile(ctx, req) (Result, error):
    devtask := get(req.NamespacedName)
    if not found: return ok

    issue := githubClient.GetIssue(devtask.spec.repo, devtask.spec.issueNumber)

    switch devtask.status.phase:
    case "":
        if issue.hasLabel("ready-for-development"):
            createNamespace(devtask)
            createNetworkPolicy(devtask)
            createSecrets(devtask)          // scoped GitHub token, ANTHROPIC_API_KEY
            createPod(devtask)              // envbuilder pod that runs claude -p
            devtask.status.phase = "Building"
            update(devtask)
        return requeueAfter(30s)

    case "Building" or "Running":
        pod := getPod(devtask)
        if pod.succeeded:
            devtask.status.phase = "AwaitingReview"
            devtask.status.prNumber = parsePRNumber(pod.logs)
        if pod.failed:
            devtask.status.phase = "Failed"
        if issue has comment matching "/clarification":
            devtask.status.phase = "BlockedOnClarification"
            deletePod(devtask)
        update(devtask)
        return requeueAfter(30s)

    case "BlockedOnClarification":
        if issue.lastComment.author != "claude-bot" and issue.hasLabel("ready-for-development"):
            // human answered, restart the task
            devtask.status.phase = ""
            update(devtask)
        return requeueAfter(1m)

    case "AwaitingReview":
        pr := githubClient.GetPR(devtask.spec.repo, devtask.status.prNumber)
        if pr.merged or pr.closed:
            deleteNamespace(devtask)
            devtask.status.phase = "Completed"
            update(devtask)
        return requeueAfter(2m)

    case "Completed" or "Failed":
        // terminal states — the DevTask sticks around for audit
        return ok
```

Polling the issue every 30 seconds from the controller is fine at POC scale. A separate ticket-watcher creating/updating CRs comes later; for now, the operator polls GitHub itself on each reconcile tick.

Simplification for laptop: if the webhook setup is annoying, drive everything from a CronJob that lists open issues with `ready-for-development` and creates/updates `DevTask` CRs. Level-triggered, so polling is semantically equivalent to webhooks.

### 4. The sandbox namespace

Per `DevTask`, operator creates `devtask-<issue-number>`. Torn down on `Completed`.

**NetworkPolicy:** deny-all egress except:
- `api.github.com` (GitHub API via MCP, git push/pull over HTTPS)
- `api.anthropic.com` (Claude API)
- kube-dns (for DNS resolution)
- `registry.npmjs.org` or equivalent for whatever the devcontainer's build phase needs

On k3d this is enforced if you install a CNI that supports it. k3d uses Flannel by default which **does not** enforce NetworkPolicies. Two options:
- Install Calico in k3d (`--k3s-arg "--flannel-backend=none@server:*"` then apply Calico manifests)
- Accept that NetworkPolicy is advisory in the POC and document that production would use Cilium

For the POC: install Calico. It's a one-time setup and gives the real experience.

**Pod security:**
- `runAsNonRoot: true`, `readOnlyRootFilesystem: true`
- Drop all caps
- `activeDeadlineSeconds: 1800` (30 min hard cap)
- Emptydir for `/workspaces`, `/tmp`

**Credentials:** mounted via projected secrets:
- `GITHUB_TOKEN`: a fine-grained PAT scoped to `jonaseck2/slaktforskning` with contents:write and issues:write. Rotated manually for now; automated rotation is a v2 concern.
- `ANTHROPIC_API_KEY`: project-scoped key

### 5. The devcontainer in `slaktforskning`

This gets checked into the repo itself. `.devcontainer/devcontainer.json`:

```json
{
  "name": "slaktforskning",
  "image": "mcr.microsoft.com/devcontainers/python:3.12",
  "features": {
    "ghcr.io/devcontainers/features/github-cli:1": {},
    "ghcr.io/anthropics/devcontainer-features/claude-code:1": {}
  },
  "postCreateCommand": "pip install -e . && pip install -r requirements-dev.txt",
  "remoteUser": "vscode"
}
```

And `.mcp.json` at the repo root, so Claude Code picks up the GitHub MCP automatically:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"
      }
    }
  }
}
```

That's the entire repo-side contract. The repo declares its environment and its tools; the pipeline invokes the agent against it.

### 6. The invocation

The operator schedules a Pod whose command is `claude -p "..."`. The canonical prompt template (stored in a ConfigMap so it's editable without rebuilding the operator):

```
You are working on GitHub issue #{issue_number} in {repo}.

Your task:
1. Read the issue and any comments using the GitHub MCP. The issue body contains an implementation plan produced by the triage step — follow it.
2. Implement the changes on a branch named `claude/issue-{issue_number}`.
3. Run tests. If they fail, iterate until they pass.
4. Commit and push. Open a PR against `main`, linking the issue.
5. Post a comment on the issue with the PR URL.

If the plan is unclear or you hit an unrecoverable blocker:
- Commit any work-in-progress to the branch
- Push the branch
- Open (or update) a draft PR
- Comment on the issue starting with "/clarification" and explain what you need
- Exit with code 2

Do not touch files outside the repo. Do not install system packages beyond what the devcontainer provides. Do not modify `.github/workflows/` or `.devcontainer/devcontainer.json` unless the issue specifically asks for that.
```

Invocation inside the pod:

```bash
cd /workspaces/slaktforskning
claude -p "$(cat /config/prompt-template.txt | envsubst)" \
  --allowedTools "Read,Edit,Write,Bash,mcp__github" \
  --dangerously-skip-permissions \
  --output-format json \
  > /tmp/claude-output.json
exit_code=$?
# pod completes; operator reads status from /tmp via a sidecar or log scraping
```

`--dangerously-skip-permissions` is needed because there's no human to approve tool calls. This is why the namespace sandbox matters — it's not about trusting the agent, it's about trusting the pod boundary.

### 7. The triage agent

Also `claude -p`, but run as a CronJob rather than per-ticket. Once every 5 minutes:

```bash
# Pseudocode
for issue in github.issues(repo, label="needs-triage", state=open):
  claude -p "Triage issue #${issue.number} in jonaseck2/slaktforskning.
    Read the issue. Assess whether it's ready for implementation.
    If yes:
      - Write a concrete implementation plan as a comment on the issue
      - Add label 'ready-for-development'
      - Remove label 'needs-triage'
    If no:
      - Comment asking for the specific missing information
      - Add label 'needs-info'
      - Remove label 'needs-triage'
    " \
    --allowedTools "mcp__github" \
    --dangerously-skip-permissions
```

Keep this even simpler than the implementation agent — triage doesn't need repo access, just GitHub API via the MCP. Run it in a namespace without network egress beyond `api.github.com` and `api.anthropic.com`.

## Implementation plan

Four days of focused work, sequenced so each step is independently verifiable. Each step ends with a concrete thing you can demo or kill.

### Day 1 — Foundations and the happy path locally, no cluster

Goal: prove Claude Code can actually maintain `slaktforskning` from a prompt. No cluster, no operator, no sandbox. Just the agent and the repo.

**Tasks:**

1. Add `.devcontainer/devcontainer.json` and `.mcp.json` to `slaktforskning`. Test by opening the repo in VS Code Dev Containers locally — confirm `claude` is on PATH inside the container and the GitHub MCP connects.
2. File a real issue on `slaktforskning` (small, well-scoped — "add a `--limit` flag to `list_persons`" or similar). Write the implementation plan directly in the issue body yourself.
3. Inside the devcontainer, run:
   ```bash
   claude -p "Implement issue #N..." \
     --allowedTools "Read,Edit,Write,Bash,mcp__github" \
     --dangerously-skip-permissions \
     --output-format json
   ```
4. Observe: does it produce a reasonable PR? Iterate on the prompt until it does on at least 3 different issue shapes (bug fix, small feature, docs update).

**Exit criteria:** `claude -p` reliably implements small issues on `slaktforskning` from a prompt, end-to-end. This is the single biggest risk — if this doesn't work, nothing else matters.

### Day 2 — k3d cluster, CRD, minimal operator, in-cluster execution

Goal: move the agent from "local devcontainer I opened by hand" to "pod the cluster spins up in response to a CR."

**Tasks:**

1. `k3d cluster create slaktforskning-poc` with a local registry.
2. Install Calico so NetworkPolicy works: `k3d cluster create ... --k3s-arg "--flannel-backend=none@server:*" --k3s-arg "--disable-network-policy@server:*"` then `kubectl apply` Calico's manifests.
3. Scaffold the operator with kubebuilder (`kubebuilder init --domain devpipeline.local`, `kubebuilder create api --group devpipeline --version v1alpha1 --kind DevTask`).
4. Implement the simplest possible controller: when a `DevTask` is created, create a namespace and a Pod in that namespace. The Pod's command is `sleep 3600`. Don't run the agent yet.
5. Wrap envbuilder. Build the `slaktforskning` devcontainer via envbuilder as a Pod in the k3d cluster, using the local registry for caching. Confirm the image builds and caches.
6. Replace the `sleep 3600` pod with an envbuilder pod that runs `claude -p "..."` with a hardcoded prompt and ticket ID.
7. Create a `DevTask` by hand (`kubectl apply -f devtask.yaml`) pointing at the real issue from Day 1. Watch the pod run through the full flow and open a PR.

**Exit criteria:** `kubectl apply -f devtask.yaml` → PR appears on `slaktforskning` within ~5 minutes.

### Day 3 — Sandbox, credentials, polling

Goal: harden the sandbox and automate the trigger.

**Tasks:**

1. Write the NetworkPolicy. Verify: a `kubectl exec` into the sandbox pod can reach `api.github.com` but not `example.com`.
2. Move credentials out of the Pod spec and into Secrets. The operator should create per-DevTask Secrets, not reuse a cluster-wide one. Scope the GitHub token as tightly as possible (fine-grained PAT, single repo).
3. Apply pod security: non-root, read-only root FS, dropped caps, `activeDeadlineSeconds`.
4. Extend the controller to poll GitHub every 30 seconds for issues labeled `ready-for-development` in `jonaseck2/slaktforskning`, create `DevTask` CRs automatically, and handle the full state machine (Pending → Building → Running → AwaitingReview → Completed).
5. Implement the `/clarification` flow: if the agent exits with code 2 and the issue has a recent `/clarification` comment, the controller transitions `DevTask` to `BlockedOnClarification` and deletes the pod. When the human responds on the issue, the controller detects the new comment and transitions back to `Pending`, which spawns a fresh pod that picks up the existing branch.

**Exit criteria:** Label an issue `ready-for-development` → wait → PR appears. No manual `DevTask` creation needed.

### Day 4 — Triage agent, cleanup, demo

Goal: close the loop and make the whole thing a one-command bring-up.

**Tasks:**

1. Implement the triage CronJob. A second `claude -p` invocation, different prompt, runs every 5 minutes against issues labeled `needs-triage`.
2. File two real issues on `slaktforskning`: one well-formed, one intentionally vague. Verify the triage agent produces an implementation plan for the first and asks for clarification on the second.
3. Write a Helm chart (or just a `kustomization.yaml`) that installs the whole pipeline: CRD, operator, triage CronJob, RBAC, ConfigMap for the prompt, Secret templates.
4. Write a `README.md` with the full bring-up: `k3d cluster create ...`, `kubectl apply -k .`, `kubectl create secret generic github-token ...`, done.
5. Demo end-to-end: file a real issue → watch it get triaged → watch it become a PR → merge → watch the namespace get cleaned up.

**Exit criteria:** `make demo` reliably produces a real PR from a real issue, with no manual intervention between filing and reviewing.

## What to watch out for

- **The devcontainer build is the slow step.** Envbuilder with a warm cache is seconds; cold is minutes. Make sure the local registry caching actually works before you get frustrated at iteration time.
- **GitHub rate limits.** Polling every 30 seconds plus MCP calls from the agent adds up. Use a GitHub App rather than a PAT if you hit limits; unauthenticated is 60/hour, PAT is 5000/hour, App is much higher.
- **Claude Code MCP discovery.** The CLI reads `.mcp.json` from the working directory. Make sure the pod's working directory is the repo root, not somewhere else, or the GitHub MCP won't be available.
- **Prompt drift across runs.** The agent will do slightly different things on slightly different prompts. Version the prompt template in the ConfigMap and log which version was used for each task.
- **The triage agent hallucinating plans.** Early iterations will produce plans that look reasonable but are subtly wrong (referencing files that don't exist, using APIs the repo doesn't have). The fix is usually in the triage prompt: tell it to read the repo structure first, cite specific files, and mark uncertainty explicitly. Consider running the triage in plan-only mode so it generates a plan file you review before the implementation agent runs against it.
- **Test failures eating the budget.** If tests are flaky, the agent will burn tokens retrying. Add a hard turn limit (`--max-turns` if Claude Code supports it by the time you build this) or a timeout wrapping `claude -p`.
- **Personal repo means you're the reviewer.** Which means there's no second set of eyes on what the agent does. For a POC on your own repo this is fine. Do not extend this pipeline to repos where other humans have shared ownership without adding mandatory-review gates.

## What comes after the POC

Prioritized rough list:

1. **Skills update PR.** Second PR per task, against `skills/`. Requires `skills/` to exist in the repo, and a second prompt at the end of the session.
2. **Multi-repo support.** Right now the repo is hardcoded; generalize to any repo with the `ai-pipeline-enabled` topic and a valid devcontainer.
3. **Webhook receiver.** Replace polling with a proper webhook receiver — smee.io for laptop testing, real ingress for anything beyond.
4. **Cost tracking.** `--output-format json` includes `cost.total_cost` per run. Store it on the `DevTask` status. Build intuition for what a PR costs before scaling up.
5. **Eval harness.** A set of reference issues with expected behaviors. Run the pipeline against them regularly to detect regressions when prompts or models change.
6. **L7 egress proxy.** Cilium with L7 policies, or an Envoy sidecar, so the sandbox can allow `github.com/jonaseck2/*` but not `github.com/*`.
7. **gVisor runtime.** Once moving off laptop k3d to a real cluster.

## A closing note on the repo itself

The `slaktforskning` repo is a genealogy tool with a SQLite database, an MCP server, and (based on context from prior conversations) probably some Python scripts around Swedish household examination records and the Ahnstedt family history. This is a particularly good POC target because:

- The work is isolated and bounded (small code surface, well-defined domain)
- It's yours, so blast radius is bounded
- The changes will be real and useful, not toy examples — you'll have actual opinions on whether the PRs are good
- It has an MCP already, which means you understand the MCP model and can think clearly about the ticketing MCP side
- It's slow-moving enough that Claude being "maintainer" for a month doesn't mean 40 PRs a week

The honest test of the pipeline is: after a month, has `slaktforskning` gotten measurably better? Are there features that exist because this pipeline existed? If yes, the architecture is sound. If no, there's a lesson in where it fell down.
