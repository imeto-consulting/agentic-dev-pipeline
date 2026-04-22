# Agentic Development Pipeline — Design 2.0

A ticket-driven, Kubernetes-native pipeline that takes a triaged ticket to a reviewable PR. Claude Code runs headless (`claude -p`) inside an ephemeral, sandboxed devcontainer built by envbuilder. The whole system is two control-plane components (one operator, one triage CronJob) and a convention for how repos opt in.

This is the scaled-out version of the laptop POC (`slaktforskning-poc-plan.md`). The POC proves the loop works on one repo; this design extends it to an organization's worth of repos without adding architectural layers.

## Design principles

**The pipeline should be as boring as possible.** The agent is the interesting part. The pipeline is a dispatch mechanism; its job is to get a prompt and a sandbox to the agent, then get out of the way. Anything the pipeline does that a devcontainer could do instead is wrong.

**Tickets are the source of truth, not a message bus.** State (triaged, ready, in-progress, blocked, done) lives on the ticket. The `DevTask` CR is a projection of that state into the cluster, not a parallel record. If they ever disagree, the ticket wins.

**Each task is hermetic.** A fresh namespace, a fresh pod, a fresh image (cached but built from spec), fresh credentials, fresh filesystem. No state leaks between tasks. Multi-task interference is prevented by construction, not by discipline.

**Repos own their environment; the pipeline owns the dispatch.** Repos declare how to be worked on (via `devcontainer.json` and `.mcp.json`). The pipeline doesn't know what languages, tools, or agents they use. Adding a repo is filing a PR that adds the required files and applies a topic; no central registry, no platform-team ticket.

**The agent CLI is the harness.** `claude -p "prompt"` is the invocation. No wrapper binary, no shim, no custom entrypoint. If something needs to run before or after, it goes in the devcontainer's lifecycle hooks.

## Architecture

```
                GitHub (org-level webhook)
                       │
                       │  (or org-level poll — identical semantics)
                       ▼
┌──────────────────────────────────────────────────────────┐
│  Cluster                                                 │
│                                                          │
│  ┌────────────────┐       ┌───────────────────────────┐  │
│  │  Triage        │       │  agentic-dev-pipeline     │  │
│  │  CronJob       │       │  operator                 │  │
│  │  (every 5 min) │       │                           │  │
│  │                │       │   watches: DevTask CRs    │  │
│  │  Runs claude   │       │   polls:   GitHub org     │  │
│  │  -p per issue  │       │                           │  │
│  │  labeled       │       │   reconciles state        │  │
│  │  needs-triage  │       │                           │  │
│  └────────┬───────┘       └────────────┬──────────────┘  │
│           │                            │                 │
│           │ labels ready-for-dev       │ creates         │
│           ▼                            ▼                 │
│  ┌──────────────────────────────────────────────────┐    │
│  │  GitHub API (issues, PRs, comments, labels)      │    │
│  │  — via ticketing MCP inside the sandbox          │    │
│  │  — via the operator's own client for polling     │    │
│  └──────────────────────────────────────────────────┘    │
│                              ▲                           │
│                              │                           │
│  ┌───────────────────────────┴────────────────────┐      │
│  │  namespace: devtask-<repo>-<issue>             │      │
│  │  ┌──────────────────────────────────────┐      │      │
│  │  │ envbuilder pod                       │      │      │
│  │  │  ├── builds from repo's              │      │      │
│  │  │  │   .devcontainer/devcontainer.json │      │      │
│  │  │  └── runs: claude -p "<prompt>"      │      │      │
│  │  │                                      │      │      │
│  │  │  .mcp.json configures ticketing MCP  │      │      │
│  │  └──────────────────────────────────────┘      │      │
│  │  NetworkPolicy: deny-all + allowlist           │      │
│  │  Pod security: non-root, read-only root FS,    │      │
│  │                dropped caps, activeDeadline    │      │
│  │  Runtime: gVisor (prod) / runc (POC)           │      │
│  │  Secrets: scoped per-task, short-lived         │      │
│  └────────────────────────────────────────────────┘      │
│                                                          │
│  Image cache: registry.agentic-dev-pipeline.svc          │
└──────────────────────────────────────────────────────────┘
```

Two components in the cluster. One CRD. Per-task namespaces that live for the duration of a task. That's the whole pipeline.

## Component design

### The `DevTask` CRD

Minimal. A ticket reference, a repo reference, a phase, and a few status fields. The operator fills in everything else during reconcile.

```yaml
apiVersion: agenticdevpipeline.dev/v1alpha1
kind: DevTask
metadata:
  name: slaktforskning-42
  namespace: agentic-dev-pipeline-system
spec:
  repo:
    owner: jonaseck2
    name: slaktforskning
  issue:
    system: github          # future: jira, linear
    number: 42
status:
  phase: Pending | Building | Running | AwaitingReview | BlockedOnClarification | Failed | Completed
  namespace: devtask-slaktforskning-42
  prNumber: 87
  branch: claude/issue-42
  startedAt: "2026-04-22T10:15:00Z"
  completedAt: null
  costUSD: "0.47"            # from claude -p --output-format json
  message: "..."
  conditions: [...]
```

Naming convention: `{repo-name}-{issue-number}`. Unique within the system namespace; stable across reconciles; greppable in logs. If the same issue is worked more than once (after merge and a reopen, say), append a sequence suffix.

### The operator

One Go binary built with kubebuilder. It does three things:

1. **Polls the GitHub org** every 30-60 seconds for issues labeled `ready-for-development` in repos carrying the `agentic-dev-pipeline-enabled` topic. Creates a `DevTask` for each one that doesn't already have a non-terminal CR.
2. **Reconciles `DevTask` CRs** through the state machine below.
3. **Watches child pods** for lifecycle events (completion, failure, timeout) and projects them into `DevTask.status`.

The poll is the primary ingestion mechanism for the scaled-out design. Webhooks are an optimization on top (latency goes from ~30s median to ~2s) that we defer until we need it — the org webhook setup is one of those things that looks cheap until you actually own it. Poll is level-triggered and therefore naturally idempotent, handles missed events for free, and doesn't need a public ingress. We can add a webhook receiver as a separate Deployment later that just creates/updates `DevTask` CRs on push — the operator stays the same.

#### State machine

```
                                 ┌─────────┐
                                 │ Pending │
                                 └────┬────┘
                                      │ create namespace,
                                      │ NetworkPolicy, Secrets,
                                      │ envbuilder Pod
                                      ▼
                                 ┌─────────┐
                                 │Building │
                                 └────┬────┘
                                      │ envbuilder completes,
                                      │ claude -p starts
                                      ▼
                                 ┌─────────┐
          ┌──────────────────────┤ Running │─────────────────────────┐
          │ pod exits 0          └────┬────┘  pod exits 2 +          │
          │ and PR URL written        │       /clarification comment │
          │ to stdout                 │                              │
          ▼                           │                              ▼
  ┌───────────────┐                   │                  ┌────────────────────────┐
  │AwaitingReview │                   │ pod exits        │ BlockedOnClarification │
  └───────┬───────┘                   │ non-zero         └────────────┬───────────┘
          │ PR merged                 ▼ (other)                       │ human
          │ or closed          ┌──────────┐                            │ comments,
          │                    │  Failed  │                            │ label stays
          ▼                    └──────────┘                            │ ready-for-dev
  ┌───────────────┐                                                    ▼
  │  Completed    │                                         (back to Pending,
  └───────────────┘                                          new pod, existing branch)
```

Transitions are driven by reconciling observed state:

- The operator re-reads the `DevTask` plus the underlying pod and issue on every tick
- Phase transitions happen when observed state changes (pod succeeded → AwaitingReview)
- Terminal states (Completed, Failed) stick around for audit; a garbage collector prunes them after 30 days

#### Clarification handoff

When the agent exits with code 2 and has posted a comment beginning with `/clarification:` on the issue, the operator:

1. Transitions `DevTask` to `BlockedOnClarification`
2. **Deletes the pod and namespace** — everything except the `DevTask` CR and the PR branch
3. Stops watching until a new non-bot comment appears on the issue

When a human responds on the issue (and the label is still `ready-for-development`), the next reconcile:

1. Transitions back to `Pending`
2. Creates a fresh namespace
3. The new pod `git checkout`s the existing branch (operator passes `CLAUDE_RESUME_BRANCH=claude/issue-42`)
4. The prompt includes "continue from the existing branch, read the latest ticket comments for clarification"

Sessions are disposable. The branch and ticket are the handoff medium. This avoids keeping expensive pods alive waiting for humans.

### The sandbox namespace

Created per task, destroyed on task completion. Everything below applies to every namespace the operator creates.

**Namespace isolation:**
- Per-task ResourceQuota (CPU: 2 cores, memory: 4Gi, pods: 1)
- Per-task LimitRange
- NetworkPolicy default-deny ingress/egress
- Service account scoped to the namespace, no cluster-level permissions

**NetworkPolicy egress allowlist:**
- kube-dns (UDP 53 to the system namespace)
- The ticketing MCP (in-cluster Service, or api.github.com for direct-to-GitHub POCs)
- The LLM gateway (in-cluster Service fronting api.anthropic.com, preferred) or the Anthropic API directly
- Approved package registries (the envbuilder build phase needs these; after build, can be tightened further)
- Nothing else

L3/L4 policy is coarse — `api.github.com` covers all GitHub API calls, including ones to other repos. For tighter scoping:

**L7 egress proxy** (production): Cilium with L7 policies, or an Envoy sidecar, so egress rules can be "api.github.com/repos/{owner}/{repo}/*" rather than "api.github.com/*". This is the real boundary in a multi-repo deployment — without it, a compromised agent working on repo A can read repo B via the same allowed IP.

**Pod security:**
- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true` (writable emptyDirs for `/workspaces`, `/tmp`, `/home/vscode/.claude`)
- `allowPrivilegeEscalation: false`
- All capabilities dropped
- `seccompProfile: RuntimeDefault` minimum
- No host network, no host PID, no hostPath mounts
- `activeDeadlineSeconds: 1800` (30 min hard cap)

**Runtime:** gVisor (`runtimeClassName: gvisor`) in production. The agent runs arbitrary code during the build phase (`npm install`, `pip install`, test commands) — treat it as untrusted. Runc is acceptable at POC scale.

**Credentials:**
- **GitHub**: fine-grained PAT or GitHub App installation token, scoped to the single repo, valid for task duration. Not a user PAT. A GitHub App is the right long-term shape — per-install tokens, easier revocation, higher rate limits.
- **Anthropic API**: project-scoped key, rate-limited at the gateway
- **MCP token**: per-task JWT signed by the operator, verified by the MCP server, expiring at task completion

All injected as projected ServiceAccount tokens or short-lived Secrets. No secrets baked into the image. No long-lived tokens in env vars that survive the pod.

### The agent invocation

The operator constructs the Pod spec with:

```yaml
spec:
  containers:
  - name: agent
    image: <envbuilder-built-image>
    workingDir: /workspaces/<repo-name>
    command: ["claude"]
    args:
      - "-p"
      - |
        You are working on GitHub issue #{{.IssueNumber}} in {{.RepoOwner}}/{{.RepoName}}.

        Read the issue via the GitHub MCP. The issue body contains an
        implementation plan produced during triage — follow it.

        Work on branch `claude/issue-{{.IssueNumber}}` (create it if needed,
        or check it out if it already exists).

        When done: run tests, push, open or update a PR against the default
        branch, link the issue, and comment on the issue with the PR URL.

        If blocked:
          - commit WIP, push the branch, open/update a draft PR
          - comment on the issue starting with "/clarification:" and explain
            what you need
          - exit with code 2
      - "--allowedTools"
      - "Read,Edit,Write,Bash,mcp__github"
      - "--dangerously-skip-permissions"
      - "--output-format"
      - "json"
    env:
      - name: GITHUB_TOKEN
        valueFrom: { secretKeyRef: { name: devtask-{{.Name}}-creds, key: github-token } }
      - name: ANTHROPIC_API_KEY
        valueFrom: { secretKeyRef: { name: devtask-{{.Name}}-creds, key: anthropic-api-key } }
```

The prompt template lives in a ConfigMap, not baked into the operator binary. Editable without a redeploy. Versioned — every `DevTask` records which prompt version ran against it, so regressions from prompt changes are attributable.

`--dangerously-skip-permissions` is needed because there's no human to approve tool calls. The namespace sandbox is the real boundary; this flag just disables an in-process check that would otherwise block the agent indefinitely.

`--output-format json` gives structured output including `cost.total_cost` and the result text. A sidecar (or the operator watching pod logs) parses this on completion to populate `DevTask.status.costUSD` and detect the PR number.

### The triage agent

Also `claude -p`, different prompt, run as a CronJob every 5 minutes:

```
For each issue in jonaseck2/* with label "needs-triage" and no label "ready-for-development":
  Read the issue, the repo structure, recent similar issues, and relevant source files.
  Decide: is this implementable as-is?

  If yes:
    - Write a concrete implementation plan as a comment on the issue
    - The plan must reference specific files, describe the approach, and state
      acceptance criteria
    - Add label "ready-for-development", remove "needs-triage"

  If no:
    - Comment asking for the specific missing information
    - Add label "needs-info", remove "needs-triage"
```

Runs in its own namespace (`agentic-dev-pipeline-triage`) with narrower network egress than the implementation pods — just `api.github.com` and `api.anthropic.com`. No repo clone, no package registries. It reads via the MCP; it doesn't need shell tools.

Triage is a good fit for a cheaper model (Sonnet or Haiku) to save cost. Implementation runs on Opus. The model choice can live in the CronJob spec and a separate prompt template.

### The repo contract

Each repo that wants to participate needs three things:

1. **The repo topic `agentic-dev-pipeline-enabled`** on the GitHub repo. This is the opt-in bit.
2. **`.devcontainer/devcontainer.json`** that installs the Claude Code devcontainer feature and any language/tool dependencies:
   ```json
   {
     "image": "mcr.microsoft.com/devcontainers/python:3.12",
     "features": {
       "ghcr.io/devcontainers/features/github-cli:1": {},
       "ghcr.io/anthropics/devcontainer-features/claude-code:1": {}
     },
     "postCreateCommand": "pip install -e . && pip install -r requirements-dev.txt",
     "remoteUser": "vscode"
   }
   ```
3. **`.mcp.json`** at the repo root:
   ```json
   {
     "mcpServers": {
       "github": {
         "command": "npx",
         "args": ["-y", "@modelcontextprotocol/server-github"],
         "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}" }
       }
     }
   }
   ```

Plus **CODEOWNERS** covering `.devcontainer/devcontainer.json`, `.mcp.json`, and `.github/workflows/*.yml` — all three files define what gets executed with elevated privileges, and a malicious or careless PR to any of them is a way out of the sandbox's intent (not the sandbox itself, but the envelope of what the sandbox is willing to do).

That's the entire repo-side API. Anything else a repo needs is part of its own devcontainer definition.

#### How opt-in actually works at scale

The operator polls the GitHub org API for repos with the topic:

```
GET /search/repositories?q=topic:agentic-dev-pipeline-enabled+org:jonaseck2
```

Result is cached for 5 minutes. For each opted-in repo, the operator lists issues with `label:ready-for-development` and reconciles accordingly. New repos become visible on the next cache refresh; removing the topic stops new tasks but doesn't interrupt running ones.

No central registry of repos. No config file that has to be kept in sync. The repo itself is the registry entry.

### Image caching

Envbuilder builds each devcontainer from its `devcontainer.json`. Cold builds are slow (minutes). Warm builds are seconds. For a multi-repo deployment, the cache has to be shared across namespaces.

Run a cluster-internal registry (`registry.agentic-dev-pipeline.svc`) and configure envbuilder's `ENVBUILDER_CACHE_REPO` to push/pull layers there. Each repo+devcontainer combination ends up with a cached image, reused across all `DevTask`s for that repo until the devcontainer definition changes.

Retention: evict images for repos that haven't had a task in 30 days. Registry storage is cheap; the cache-miss cost is seconds per task; the eviction is just housekeeping.

## Scaling to multiple repos: what changes

Almost nothing. The single-repo POC and the multi-repo production deployment are architecturally identical — the operator polls the org instead of one repo, the image cache is shared, the NetworkPolicy allowlist might get an L7 proxy for per-repo scoping.

What genuinely changes:

| Concern | Single-repo POC | Multi-repo production |
|---|---|---|
| Opt-in | Hardcoded repo in operator | Repo topic `agentic-dev-pipeline-enabled` |
| GitHub auth | Personal fine-grained PAT | GitHub App with per-install tokens |
| Poll target | One repo | Org search for topic |
| NetworkPolicy | Allow api.github.com | Allow api.github.com + L7 rules per repo |
| Image cache | Local registry, one image | Cluster registry, per-repo images |
| Secrets | One Secret | Operator mints per-task from GitHub App |
| Runtime | runc on k3d | gVisor on a real cluster |
| Rate limits | Probably fine | GitHub App gives higher limits; monitor |
| Observability | `kubectl logs` | Prometheus metrics, alerting on Failed rates |
| Reviewer capacity | You | Becomes the real bottleneck |

None of these are architecture changes. They're operational hardening on the same shape.

### The cost thing

`claude -p --output-format json` emits `cost.total_cost` in USD per run. The operator writes this to `DevTask.status.costUSD`. That gives:

- Per-task cost, visible in `kubectl get devtasks -o wide`
- Per-repo cost via label aggregation (Prometheus recording rules)
- Per-day cost trend
- Budget alerts when a repo exceeds a threshold

Cost tracking is not a day-one need but it's trivial to wire up and valuable once multiple repos are live. Budget overruns are the most common "oh no" in agentic systems; know where they come from before they happen.

### The review bottleneck

At one repo, you can review everything the agent produces. At ten repos, you can't. The pipeline doesn't solve this — it makes it the gating constraint. Worth building in before it bites:

- **Mandatory human review on every PR.** No auto-merge, regardless of CI green. CODEOWNERS on every path means every PR has a required reviewer.
- **Rate limits per repo.** Configurable max concurrent `DevTask`s per repo (default 1) and max tasks per day (default 5). Prevents a mass-triage from generating 50 PRs overnight that no one will review.
- **Label-based throttling.** `agentic-dev-pipeline-paused` label on a repo → the operator skips that repo entirely. Emergency off switch per-repo without touching the cluster.
- **Aging metrics.** Time in `AwaitingReview` is a review-latency signal. If it's climbing, either review is overloaded or the agent is producing bad PRs that stall in review — both are important to know.

## What's out of scope for 2.0

These are explicitly deferred. Each is defensible but none is load-bearing.

- **Skills update PR.** The "agent proposes a PR against `skills/` with what it learned this task" loop. Valuable but requires a clear evaluation story to avoid skills rot. v2.1.
- **Cross-repo work.** A ticket that touches multiple repos. Out of scope — we have the single-repo case to get right first.
- **Non-GitHub ticketing.** Jira, Linear, ServiceNow. The architecture supports them (`issue.system` is already in the CRD), but each ticketing MCP is its own integration. Add on demand.
- **Multi-agent.** Multiple agents collaborating on one task, Gas Town / Multiclaude-style. The single-agent case isn't fully exploited yet.
- **Cost-based model routing.** "Use Haiku for this, Opus for that, based on predicted complexity." Requires data we don't have yet.
- **Self-hosted models.** Claude via Bedrock, self-hosted Llama, etc. The devcontainer contract already permits this (the agent is whatever the repo installs), but the operator's cost accounting assumes Anthropic-formatted JSON output. Solvable but not now.

## Open questions

These need answers before going beyond the POC:

- **Webhook or poll.** Start with poll (simpler, no ingress, naturally idempotent). Add a webhook receiver as a thin Deployment if latency starts mattering. What's the latency budget that makes this a real problem?
- **GitHub App vs GitHub PAT.** App is the right long-term answer but adds setup friction. PAT is fine for POC and early production on a single user's repos. Migration isn't hard but should happen before the first "other human's repo" onboards.
- **The L7 egress proxy.** Cilium network policies, Envoy sidecar, or a dedicated proxy Service in each namespace? Cilium is the most Kubernetes-native but requires Cilium as the CNI cluster-wide. Envoy sidecar is per-pod overhead. A proxy Service per namespace works but adds latency. Worth benchmarking.
- **Triage prompt stability.** Early triage runs will produce plans that look reasonable but are subtly wrong (referencing non-existent files, using APIs the repo doesn't have). How much evaluation infrastructure do we need before triage is trusted enough to auto-apply `ready-for-development`? Starting with human approval of every triage output is the safe default.
- **What counts as "done"?** Currently: PR merged. But the agent should arguably learn from merge vs. reject vs. changes-requested signals. Capturing this feedback into the skills loop is v2.1 but the data collection should start now.

## Summary

Two components, one CRD, namespace-per-task sandbox, headless `claude -p` as the agent, repos opt in via topic and three files. Scaling from one repo to many is mostly operational (auth, rate limits, L7 egress, observability) rather than architectural. Tickets hold the plan and the state; the cluster holds the ephemeral execution; repos hold the environment definition; and none of them tries to hold the others' concerns.

If the design ever starts growing new components — a scheduler, a cache service, a plan DB, a session manager — that's the signal that something has been mislocated. Send it back to the ticket, the devcontainer, or the operator's reconcile loop.
