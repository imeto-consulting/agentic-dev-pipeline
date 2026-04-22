# Architecture

## System Overview

Two components run in the cluster. One CRD. Per-task namespaces live for the duration of a task. That's the entire pipeline.

```
           GitHub Issues                    (github.com)
               │
               │  polling every 30s (webhook in v2)
               ▼
┌──────────────────────────────────────────────────┐
│  k3d cluster (laptop)                            │
│                                                  │
│  ┌─────────────────┐   ┌────────────────────┐    │
│  │  triage         │   │  operator          │    │
│  │  CronJob        │   │  (kubebuilder)     │    │
│  │  every 5 min    │   │                    │    │
│  │                 │   │  watches DevTask   │    │
│  │  claude -p per  │   │  polls GitHub      │    │
│  │  needs-triage   │   │  reconciles state  │    │
│  │  issue          │   │                    │    │
│  └────────┬────────┘   └──────────┬─────────┘    │
│           │                       │              │
│           │ labels ready-for-dev  │ creates      │
│           ▼                       ▼              │
│  ┌────────────────────────────────────────────┐  │
│  │  GitHub API (issues, PRs, comments, labels)│  │
│  └────────────────────────────────────────────┘  │
│                           ▲                      │
│                           │ MCP + git push        │
│  ┌────────────────────────┴────────────────────┐  │
│  │  namespace: devtask-<issue-number>          │  │
│  │  ┌──────────────────────────────────────┐   │  │
│  │  │  envbuilder pod                      │   │  │
│  │  │  ├─ builds slaktforskning devcontainer│  │  │
│  │  │  └─ runs: claude -p "<prompt>"       │   │  │
│  │  └──────────────────────────────────────┘   │  │
│  │  NetworkPolicy: deny-all egress             │  │
│  │    + api.github.com                         │  │
│  │    + api.anthropic.com                      │  │
│  │    + kube-dns                               │  │
│  │    + package registries (build phase)       │  │
│  └─────────────────────────────────────────────┘  │
│                                                  │
│  Image cache: slaktforskning-registry:5000       │
└──────────────────────────────────────────────────┘
```

## Components

### DevTask CRD

Minimal shape. Ticket reference, repo reference, status. The CR is derived state — the operator creates it when an issue hits `ready-for-development`, deletes it (and its namespace) when the PR merges or the task fails permanently.

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

### Operator (kubebuilder)

One Go binary. Three responsibilities:

1. **Polls GitHub** every 30s for issues labeled `ready-for-development`. Creates a `DevTask` for each one without a non-terminal CR.
2. **Reconciles DevTask CRs** through the state machine (see below).
3. **Watches child pods** — completion/failure/timeout projected to `DevTask.status`.

### Triage CronJob

`claude -p` every 5 minutes against issues labeled `needs-triage`. Runs in namespace `agentic-dev-pipeline-triage` with narrower egress (no package registries, no repo clone). Uses a cheaper model (Sonnet). If the issue is ready: writes a concrete implementation plan as a comment, applies `ready-for-development`. If not: asks for clarification, applies `needs-info`.

### Sandbox Namespace

Per `DevTask`. Created on Pending→Building transition, destroyed on Completed/Failed.

**NetworkPolicy** — deny-all egress except:
- kube-dns (UDP 53)
- `api.github.com` (GitHub API via MCP, git push/pull)
- `api.anthropic.com` (Claude API)
- Package registries during build phase (npmjs.org etc.)

**Pod security** — `runAsNonRoot: true`, `readOnlyRootFilesystem: true`, all caps dropped, `activeDeadlineSeconds: 1800`.

**Credentials** — per-task Secrets (not cluster-wide). GitHub fine-grained PAT scoped to single repo. Anthropic project-scoped key.

### envbuilder Pod

Builds the `slaktforskning` devcontainer from `.devcontainer/devcontainer.json` using envbuilder. Layer cache pushed to the local registry (`slaktforskning-registry:5000`). Cold build: minutes. Warm build: seconds.

Once built, runs `claude -p "<prompt>"` with the prompt template from a ConfigMap.

## State Machine

```
             ┌─────────┐
             │ Pending │  ← operator creates on ready-for-development label
             └────┬────┘
                  │ create namespace, NetworkPolicy, Secrets, envbuilder Pod
                  ▼
             ┌─────────┐
             │Building │  ← envbuilder building devcontainer image
             └────┬────┘
                  │ build complete, claude -p starts
                  ▼
             ┌─────────┐
┌────────────┤ Running │──────────────────────────────┐
│ pod exits 0└────┬────┘ pod exits 2 +                │
│ PR URL in        │      /clarification comment       │
│ stdout           │                                   │
▼                  │ pod exits nonzero                  ▼
┌───────────────┐  │  (other)          ┌────────────────────────┐
│AwaitingReview │  ▼                   │BlockedOnClarification  │
└───────┬───────┘ ┌──────────┐         └────────────┬───────────┘
        │ PR      │  Failed  │          human        │
        │ merged  └──────────┘          comments,    │
        │ or closed                     label stays  │
        ▼                               ▼
┌───────────────┐              (back to Pending,
│  Completed    │               new pod, existing branch)
└───────────────┘
```

**Clarification handoff:** When the agent exits 2 with a `/clarification:` comment on the issue, the operator deletes the pod and namespace (not the CR), transitions to `BlockedOnClarification`, and waits. When a human responds on the issue, the next reconcile transitions back to Pending and spawns a fresh pod. The fresh pod receives `CLAUDE_RESUME_BRANCH=claude/issue-42` and a prompt variant that says "continue from the existing branch, read ticket for clarification answers."

## Agent Invocation

The operator constructs the Pod spec with the prompt template from a ConfigMap. The canonical prompt:

```
You are working on GitHub issue #{issueNumber} in {repo}.

Your task:
1. Read the issue and comments via the GitHub MCP. The issue body contains an implementation plan — follow it.
2. Work on branch `claude/issue-{issueNumber}` (create or check out as needed).
3. Run tests. Iterate until they pass.
4. Commit and push. Open a PR against main, linking the issue.
5. Post a comment on the issue with the PR URL.

If blocked or plan is unclear:
- Commit WIP, push the branch, open/update a draft PR
- Comment on the issue starting with "/clarification:" and explain what you need
- Exit with code 2
```

Invocation flags: `--allowedTools "Read,Edit,Write,Bash,mcp__github"`, `--dangerously-skip-permissions`, `--output-format json`.

## Repo Contract (slaktforskning side)

The `slaktforskning` repo needs:

1. `.devcontainer/devcontainer.json` — must install `claude` CLI (via the Anthropic devcontainer feature or the Dockerfile)
2. `.mcp.json` — must include the GitHub MCP server so `claude -p` can read issues and open PRs
3. `CODEOWNERS` — covering `.devcontainer/`, `.mcp.json`, `.github/workflows/` (security: these files define what executes with elevated privileges)

The pipeline doesn't know what language or tools the repo uses — that's the devcontainer's job.

## Security Model

- **Namespace boundary:** Each task gets its own namespace. No shared storage, no shared service accounts.
- **NetworkPolicy:** Calico enforces egress. The agent can only reach pre-approved endpoints.
- **Pod security:** Non-root, read-only rootFS, no privilege escalation, all caps dropped.
- **Credentials:** Per-task Secrets from fine-grained PATs. Not cluster-wide. Scoped to single repo, single task duration.
- **`--dangerously-skip-permissions`:** Disables the in-process approval check. This is intentional — the namespace sandbox replaces it. The flag name is scary; the security comes from the pod boundary.
- **POC note:** NetworkPolicy enforcement requires Calico. k3d's default Flannel does not enforce it. Install Calico on cluster creation.

## Image Cache

envbuilder pushes built layers to `slaktforskning-registry:5000` (k3d local registry). Set via `ENVBUILDER_CACHE_REPO=slaktforskning-registry:5000/slaktforskning-devcontainer`. Cache hits are per layer; a small code change in `postCreateCommand` may bust part of the cache but reuse base image layers.

Without the local registry, every task rebuilds from scratch and iteration time becomes unbearable.
