# GitHub App Setup

Phase 5 Task 3 replaces the long-lived fine-grained PAT with a GitHub App
that mints short-lived installation tokens per DevTask. This document is the
deterministic reference for creating that App and installing it on the target
repo.

The App permissions and event subscriptions are codified in
[`config/github-app-manifest.json`](../config/github-app-manifest.json) so
recreating the App in a different account/org gives the same configuration.

## One-time: register the App

GitHub does not expose App registration via REST, so this step is web-only.
Use the manifest flow so the permissions match the file in this repo.

1. Open <https://github.com/settings/apps/new>. (Personal account; for an org
   App, use `https://github.com/organizations/<org>/settings/apps/new`.)
2. Set:
   - **GitHub App name**: `agentic-dev-pipeline-<your-handle>` — names are
     globally unique, suffix with your handle.
   - **Homepage URL**: `https://github.com/jonaseck2/agentic-dev-pipeline`
   - **Webhook**: uncheck **Active**. The pipeline polls; it does not consume
     webhook events.
   - **Repository permissions** — set exactly these, leave the rest at "No
     access":
     - Contents: **Read and write**
     - Issues: **Read and write**
     - Pull requests: **Read and write**
     - Metadata: **Read-only** (auto-set; required by GitHub)
   - **Subscribe to events**: leave all unchecked.
   - **Where can this GitHub App be installed?**: **Only on this account**
     (private/personal use). Switch to **Any account** later if the pipeline
     becomes a shared service.
3. Click **Create GitHub App**.
4. On the next page, **Generate a private key** (PEM file). Save it locally
   as `~/.config/agentic-dev-pipeline/github-app.pem` with `chmod 600`. This
   is the long-lived secret that replaces the old PAT.
5. Note the **App ID** (top of the settings page).

## One-time: install on the target repo

1. From the App settings page, click **Install App** in the left sidebar.
2. Choose your account, **Only select repositories**, pick `slaktforskning`
   (or whichever repo you're targeting).
3. After install, GitHub redirects to a URL like
   `https://github.com/settings/installations/<INSTALLATION_ID>`. Note the
   **Installation ID** — you'll need it for `make secrets`.

## Verifying the App permissions match the manifest

```bash
APP_SLUG=agentic-dev-pipeline-<your-handle>
gh api /apps/$APP_SLUG --jq '{name, permissions}'
```

Compare with `config/github-app-manifest.json` — the `permissions` block
must match exactly. If you accidentally added something extra (e.g. `actions:
write`), revoke it from the App settings UI and re-run the verify command.

## Per-deploy: provide credentials to the pipeline

`make secrets` (Phase 5 onwards) reads three new env vars instead of
`GITHUB_TOKEN`:

```bash
export GH_APP_ID=<app id from step 5>
export GH_INSTALLATION_ID=<installation id from the install redirect>
export GH_APP_PRIVATE_KEY_PATH=$HOME/.config/agentic-dev-pipeline/github-app.pem

# CLAUDE_TOKEN, GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL unchanged
make secrets
```

The operator mints a fresh installation token (~1h TTL) on every reconcile
and writes it into the per-DevTask Secret immediately before spawning the
agent pod. Tokens are not persisted across reconciles.

## Rotation

Private key rotation is a manual web step. Generate a new PEM via the App
settings page, replace the file at `GH_APP_PRIVATE_KEY_PATH`, re-run `make
secrets`. The old key keeps working until you delete it from the GitHub UI.

## Why not the manifest URL flow

GitHub also offers an [App Manifest Flow][manifest-flow] that POSTs the
manifest JSON, redirects the user to confirm in the browser, and returns the
App credentials to a callback URL. We don't use it because:

- It requires a temporary HTTP server to capture the redirect — extra moving
  part for a one-time setup.
- The user still has to click through the GitHub UI to confirm, so it's not
  more deterministic than the form-fill approach above.
- Permissions in our manifest file remain authoritative either way.

[manifest-flow]: https://docs.github.com/en/apps/creating-github-apps/setting-up-a-github-app/creating-a-github-app-from-a-manifest
