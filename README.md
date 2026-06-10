# Agentic Development Pipeline — POC

Claude as maintainer of any GitHub repository, running on a local k3d cluster.

File an issue → triage agent writes an implementation plan → implementation agent opens a PR → merge → namespace cleaned up. No manual steps between filing and reviewing.

This repo is a **template**. You point it at your own target repo, run a few `make` commands, and the pipeline starts watching that repo for issues to work on.

## Bring-up

**Prerequisites:** `brew install k3d kubectl kubebuilder helm go gh docker gettext`

(`gettext` provides `envsubst`, used to render manifests.)

```bash
# 0. One-time: write your local config (target repo, cluster + registry names)
make init

# 1. Create the k3d cluster (k3d + Calico CNI)
make cluster

# 2. Seed the in-cluster registry with the devcontainer image. Required before
#    any triage or agent pod can start — they all pull this image.
make seed-image

# 3. Set credentials and create the in-cluster secret
export GITHUB_TOKEN=$(gh auth token)         # or a fine-grained PAT
export CLAUDE_OAUTH_TOKEN="sk-ant-oat01-..." # subscription billing
# OR (mutually exclusive):
# export CLAUDE_TOKEN="sk-ant-..."           # API billing
export GIT_AUTHOR_NAME="Your Name"
export GIT_AUTHOR_EMAIL="you@example.com"
make secrets

# 4. Install CRDs and pipeline components
make install

# 5. Run the operator locally against the cluster
make run
```

The fine-grained PAT (or `gh` token) needs Contents, Issues, and Pull Requests
read+write on the target repo.

## Auth modes

- `CLAUDE_OAUTH_TOKEN` — your personal Claude Code subscription (Pro / Team). Recommended for local development so you don't burn API credits.
- `CLAUDE_TOKEN` — pay-per-token API key. Recommended for shared CI/CD setups.

`make secrets` rejects setups where both (or neither) is set.

## Demo

```bash
make demo
```

Files a real issue with the `needs-triage` label. The triage CronJob picks it
up within 5 minutes (or run `make triage` to trigger immediately), writes an
implementation plan, and applies `ready-for-development`. The operator detects
the label within 30 seconds and starts an agent pod to implement it.

## Manual triage trigger

```bash
make triage
```

## Reviewing plans

The triage agent self-classifies each plan. If the plan it writes proposes
touching sensitive surface — `.github/`, `.devcontainer/`, `Dockerfile`,
`package.json`, `.mcp.json`, `deploy/`, `operator/`, or anything matching
`secret` / `credential` / `token` / `apikey` — it labels the issue
**`needs-plan-review`** instead of `ready-for-development`, and the operator
does **not** spawn an impl agent.

To act on a `needs-plan-review` issue: read the most recent `Implementation
plan: …` comment, then either

- relabel `needs-plan-review` → `ready-for-development` to let the impl agent
  run (the operator picks it up within 30s), or
- close the issue (or ask for changes) if the plan is unacceptable.

This is the human checkpoint before any Claude tokens are spent on a
potentially poisoned plan. It pairs with the operator's diff policy (which
rejects the resulting PR if it actually touches restricted paths) — the label
gate catches intent early, the diff policy catches it at the PR. On a public
repo, treat issue text as untrusted and read these plans carefully.


## Configuration

All target-repo and infra naming lives in `.pipeline.env` (gitignored). Run
`make init` to (re)generate it interactively, or copy `.pipeline.env.example`
and edit by hand. `make check-config` validates that everything required is set
before you spin up a cluster.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md).
