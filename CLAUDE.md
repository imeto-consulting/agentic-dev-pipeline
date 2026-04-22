# Agentic Development Pipeline

Kubernetes-native pipeline that takes a triaged GitHub issue to a reviewable PR. `claude -p` runs headless inside an ephemeral sandboxed devcontainer built by envbuilder. Two cluster components: a triage CronJob and an operator. One CRD: `DevTask`.

## What this repo contains

This repo is the **pipeline** (operator, triage agent, cluster config). The repo being maintained by the pipeline is `jonaseck2/slaktforskning` at `/Users/jonasahnstedt/git/slaktforskning`.

```
docs/
  plans/
    ROADMAP.md                          — milestone map
    2026-04-22-poc-phase*.md            — active implementation plans
    archive/                            — finished plans
    design/
      agentic-dev-pipeline-poc-design.md   — POC spec
      agentic-dev-pipeline-design.md       — v2.0 full design
CLAUDE.md                               — this file
ARCHITECTURE.md                         — system architecture reference
.claude/
  skills/                               — development skills for this repo
operator/                               — kubebuilder Go project (created in Phase 2)
deploy/                                 — kustomization / Helm chart (created in Phase 4)
```

## Tech stack

| Layer | Technology |
|---|---|
| Cluster | k3d (laptop) |
| CNI | Calico (NetworkPolicy enforcement) |
| Operator framework | kubebuilder v3, Go |
| Container builder | envbuilder (Coder) |
| Agent | Claude Code (`claude -p`) |
| Ticketing | GitHub Issues via `@modelcontextprotocol/server-github` MCP |

## Prerequisites

```bash
brew install k3d kubectl kubebuilder helm go
```

## Cluster bring-up

```bash
k3d cluster create slaktforskning-poc \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create slaktforskning-registry:5000 \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml
```

## Operator development

```bash
cd operator/
make generate     # regenerate after type changes
make manifests    # update CRD YAML
make install      # apply CRDs to cluster
make run          # run controller locally against cluster
```

## Working with plans

- Active plans: `docs/plans/2026-*-*.md`
- Finished plans: move to `docs/plans/archive/`
- Roadmap: `docs/plans/ROADMAP.md` — update checkboxes as phases complete
- To execute a plan: use the `superpowers:executing-plans` or `superpowers:subagent-driven-development` skill

## Key design decisions

**No Coder, no wrapper binary.** envbuilder alone builds the devcontainer; `claude -p` is the invocation. Coder is overkill for the POC; add it later if workspace UI is needed.

**Polling, not webhooks.** Level-triggered poll every 30s is semantically equivalent to webhooks and requires no public ingress. Webhook receiver is a future optimization once latency matters.

**Tickets are source of truth.** `DevTask` CR is a projection of GitHub issue state. If CR and issue disagree, the ticket wins.

**`--dangerously-skip-permissions` inside the sandbox.** The namespace NetworkPolicy is the real security boundary. The in-process check would block the agent indefinitely with no human present to approve.

**Calico, not Flannel.** k3d ships with Flannel which does not enforce NetworkPolicy. Calico is a one-time cluster setup and gives real enforcement for the sandbox egress allowlist.

## Skills for this repo

`.claude/skills/` contains:

- `k3d-cluster-ops.md` — creating and managing the k3d cluster with Calico
- `kubebuilder-operator.md` — working with the DevTask kubebuilder operator
- `envbuilder-devcontainer.md` — envbuilder layer caching and devcontainer builds
