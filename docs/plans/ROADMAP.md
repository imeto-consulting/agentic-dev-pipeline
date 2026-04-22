# Agentic Development Pipeline

Kubernetes-native pipeline that takes a triaged GitHub issue to a reviewable PR. `claude -p` runs headless inside an ephemeral sandboxed devcontainer built by envbuilder. Two cluster components: a triage CronJob and an operator. One CRD: `DevTask`.

## What this repo contains

This repo is the **pipeline** (operator, triage agent, cluster config). The repo being maintained by the pipeline is `jonaseck2/slaktforskning` at `/Users/jonasahnstedt/git/slaktforskning`.

```
docs/
  plans/
    ROADMAP.md                  — milestone map
    2026-04-22-poc-phase*.md    — implementation plans (active)
    archive/                    — finished plans
    design/
      agentic-dev-pipeline-poc-design.md   — POC spec
      agentic-dev-pipeline-design.md       — v2.0 design
CLAUDE.md                       — this file
ARCHITECTURE.md                 — system architecture reference
.claude/
  skills/                       — development skills for working on this repo
```

## Tech stack

| Layer | Technology |
|---|---|
| Cluster | k3d (laptop), k3s |
| CNI | Calico (NetworkPolicy enforcement) |
| Operator framework | kubebuilder v3 |
| Container builder | envbuilder (Coder) |
| Agent | Claude Code (`claude -p`) |
| Ticketing | GitHub Issues via `@modelcontextprotocol/server-github` |
| Language | Go (operator), shell (cluster setup) |

## Local development setup

### Prerequisites

```bash
brew install k3d kubectl kubebuilder helm
```

### Cluster bring-up

```bash
k3d cluster create slaktforskning-poc \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create slaktforskning-registry:5000 \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

# Install Calico for NetworkPolicy enforcement
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml
```

### Operator development

```bash
cd operator/
make generate          # regenerate CRD manifests after type changes
make manifests         # update CRD YAML
make install           # apply CRDs to the cluster
make run               # run controller locally against the cluster
make docker-build      # build the operator image
```

### Running the full pipeline locally

```bash
kubectl create secret generic pipeline-creds \
  --from-literal=github-token=<fine-grained-PAT> \
  --from-literal=anthropic-api-key=<key> \
  -n agentic-dev-pipeline-system

kubectl apply -k deploy/
```

## Working on plans

- Active plans: `docs/plans/2026-*-*.md`
- Finished plans: move to `docs/plans/archive/`
- ROADMAP: `docs/plans/ROADMAP.md` — update checkboxes as phases complete

## Key design decisions

**No Coder, no wrapper binary.** envbuilder alone builds the devcontainer; `claude -p` is the invocation. See `docs/plans/design/agentic-dev-pipeline-poc-design.md` for rationale.

**Polling, not webhooks.** Level-triggered poll every 30s is semantically equivalent to webhooks and requires no public ingress. Webhook receiver is a future optimization.

**Tickets are source of truth.** `DevTask` CR is a projection of GitHub issue state. If they disagree, the ticket wins.

**`--dangerously-skip-permissions` inside the sandbox.** The namespace NetworkPolicy is the real security boundary. The in-process permission check would block the agent indefinitely with no human to approve.

## Skills

Development skills for working on this repo live in `.claude/skills/`. Invoke them with the `Skill` tool or `/skill-name`.
