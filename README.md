# Agentic Development Pipeline — POC

Claude as maintainer of [`slaktforskning`](https://github.com/jonaseck2/slaktforskning), running on a local k3d cluster.

File an issue → triage agent writes an implementation plan → implementation agent opens a PR → merge → namespace cleaned up. No manual steps between filing and reviewing.

## TL;DR — let Claude run the demo for you

The fastest way to see this work is to ask Claude Code (in this repo) to run the
whole demo. Open a session with `auto` mode in this repo and paste:

> Run the agentic dev pipeline demo end-to-end. Create the k3d cluster if it
> doesn't exist, install CRDs and components, seed the in-cluster registry with
> the slaktforskning devcontainer image, create the secrets from my `~/.zshrc`
> credentials and `gh auth token`, start the operator in the background, file a
> demo issue, trigger the triage job, then watch until a PR is opened on
> `jonaseck2/slaktforskning`. Fix any failures you hit and report the PR URL.

Claude will work through the steps below, retry on transient failures, and
report the PR URL at the end. Watch its tool calls and intervene if you want
to redirect.

## Manual bring-up (if you'd rather drive it yourself)

**Prerequisites:** `brew install k3d kubectl kubebuilder helm go gh docker`

```bash
# 1. Create cluster (k3d + Calico CNI)
make cluster

# 2. Install CRDs and pipeline components
make install

# 3. Seed the in-cluster registry with the slaktforskning devcontainer image.
#    On a fresh cluster this is required before any triage or agent pod can
#    start — they all pull this image.
make seed-image

# 4. Set credentials and create the in-cluster Secret
export GITHUB_TOKEN=$(gh auth token)            # or a fine-grained PAT
export CLAUDE_TOKEN=$CLAUDE_CODE_OAUTH_TOKEN    # from your shell rc
export GIT_AUTHOR_NAME="Your Name"
export GIT_AUTHOR_EMAIL="you@example.com"
make secrets
```

The fine-grained PAT (or `gh` token) needs: Contents Read+Write, Issues
Read+Write, Pull Requests Read+Write on `jonaseck2/slaktforskning`.

## Running the operator

```bash
make run
```

Runs the operator locally against the cluster. Leave this terminal open.

## Demo

```bash
make demo
```

Files a real issue with `needs-triage` label. The triage CronJob picks it up
within 5 minutes (or trigger immediately — see below), writes an implementation
plan, and applies `ready-for-development`. The operator detects the label
within 30 seconds and starts an agent pod to implement it.

## Manual triage trigger

```bash
kubectl create job --from=cronjob/triage-agent triage-manual \
  -n agentic-dev-pipeline-triage
kubectl logs -n agentic-dev-pipeline-triage job/triage-manual --follow
```

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/plans/ROADMAP.md](docs/plans/ROADMAP.md).
</content>
</invoke>