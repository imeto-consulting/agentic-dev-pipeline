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

## GitHub App auth (recommended for public repos)

Instead of mounting a long-lived PAT into every agent pod, the operator can
authenticate as a **GitHub App installation**: it mints a short-lived (~1 hour)
installation token per task and writes it into that task's namespace Secret. A
token leaked from a prompt-injected agent then expires within the hour and is
scoped to a single installation — a far smaller blast radius than a PAT.

1. Register the App and install it on the target repo following
   [docs/github-app-setup.md](docs/github-app-setup.md). Permissions are
   codified in [config/github-app-manifest.json](config/github-app-manifest.json):
   Contents RW, Issues RW, Pull requests RW, Metadata R — nothing else.
2. Save the App ID, the installation ID, and the private-key PEM
   (`chmod 600`), then set in `.pipeline.env` (or your shell):

   ```bash
   export GH_APP_ID=123456
   export GH_INSTALLATION_ID=12345678
   export GH_APP_PRIVATE_KEY_PATH=~/.config/agentic-dev-pipeline/github-app.pem
   make secrets
   ```

`make secrets` creates a `pipeline-app-key` Secret. When it is present the
operator uses minted installation tokens; when it is absent it falls back to
`GITHUB_TOKEN`. In App mode the operator also **rotates the triage CronJob's
token**: it refreshes the `github-token` in the triage namespace's
`pipeline-creds` Secret with a fresh installation token every 45 minutes, so
the triage job no longer relies on a long-lived PAT either.

`GITHUB_TOKEN` is still set by `make secrets` as the PAT-mode fallback and the
initial triage value before the first rotation. Verify the live setup with
`scripts/verify-hardening.sh` (checks the triage token is a `ghs_…` App token,
among other controls) or `gh api /rate_limit` using a minted token (identity
shows the App, not a user).

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

## Egress allowlist (optional, recommended for public repos)

By default an agent pod may reach any host on `:443`. That is enough for a
prompt-injected agent to POST a token to an attacker's server. The
`deploy/egress-proxy/` manifests deploy a Squid forward proxy with a
CONNECT-domain allowlist (github.com, anthropic.com, npm/pypi by default) and
flip the model to **proxy-only egress**: the agent's NetworkPolicy denies all
direct `:443`, so the allowlist is the only way out.

```bash
kubectl apply -f deploy/egress-proxy/        # namespace, configmap, deployment, service, networkpolicy
# Then point the operator at the proxy and restart it:
export EGRESS_PROXY_URL=http://egress-proxy.egress-proxy.svc.cluster.local:3128
make run
```

When `EGRESS_PROXY_URL` is set the operator (a) tightens each task's
NetworkPolicy to DNS + the proxy namespace only, and (b) injects
`HTTPS_PROXY`/`HTTP_PROXY`/`NO_PROXY` into the agent containers. Unset, behavior
is unchanged. Edit the allowlist in `deploy/egress-proxy/configmap.yaml` to match
your target repo's toolchain — keep it as tight as the build actually needs.
Requires Calico (NetworkPolicy enforcement).

`scripts/verify-hardening.sh` probes the live cluster to confirm Calico actually
enforces the policy — that an allowlisted host is reachable through the proxy
while a non-allowlisted host and direct `:443` are blocked.

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

## Verifying the security controls

- **Unit tests** (`make -C operator test`) prove the static shape: the diff
  policy's path/size rules, the NetworkPolicy egress shape in both modes, the
  agent pod's security context (no SA token, non-root, read-only FS, caps
  dropped, resource limits), and the triage-token rotation logic.
- **`scripts/verify-hardening.sh`** proves the live-cluster runtime behavior
  unit tests can't: that Calico enforces the egress policy, that the operator's
  applied RBAC can rotate the triage Secret, that the triage token is an App
  installation token, and that the egress proxy is healthy.
- **Manual checklist** for the three controls that depend on a (non-deterministic)
  live agent run — file a demo issue and confirm each: a PR touching
  `.github/` is auto-closed by the diff policy; a plan mentioning a sensitive
  path is labeled `needs-plan-review` rather than `ready-for-development`; a
  `/clarification` answered by a non-collaborator does **not** resume the agent.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md).
