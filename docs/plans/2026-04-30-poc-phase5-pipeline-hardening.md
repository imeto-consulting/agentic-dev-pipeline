# POC Phase 5 — Pipeline Hardening for Public Release

> **For agentic workers:** Sub-tasks are checkbox items. Land each task as its own PR; do not bundle. Run the demo end-to-end after each task to confirm no regression.

**Goal:** Close the supply-chain pivot before this repo is public. Ship the smallest set of changes that prevents an attacker who can file a `needs-triage` issue from landing malicious code via the agent — without adding so much friction that the pipeline stops being useful.

**Non-goals:** multi-repo support, multi-tenancy, cost caps, GUI dashboards, eval infra. Those belong in v2.0 (`docs/plans/design/agentic-dev-pipeline-design.md`). This plan is just the security floor.

**Threat model — what we're defending against:**

1. Attacker files an issue containing prompt injection. Triage agent's plan reflects the injection. Impl agent acts on the poisoned plan.
2. Attacker's payload lands in a PR that modifies `.github/workflows/`, `package.json` deps, `Dockerfile`, or other CI/CD-adjacent files. Maintainer merges without scrutinizing the diff. Attacker now has code execution in the repo's CI environment with whatever secrets the workflow exposes.
3. The single fine-grained PAT used by every DevTask has full Contents/Issues/PRs RW on the target repo. A compromised agent leaks it (commit message, PR description, source file) or uses it to push to `main` directly.

The three mitigations in this plan, in order of how much each one closes:

- **Path + diff-size policy on the impl agent's PR** — closes the CI/CD pivot directly.
- **Label-gated plan review for risk-pattern matches** — adds a human checkpoint before Claude tokens get spent on a poisoned plan.
- **GitHub App tokens in place of the long-lived PAT** — limits blast radius of any token leak; bonus: per-installation rate limits.

Plus a Task 0 that pins the target-repo configuration to a script so the labels and branch-protection settings the other tasks depend on aren't drifty hand-clicked state.

---

## Task 0: Deterministic target-repo configuration

**Why first:** Tasks 1 and 2 introduce new labels (`needs-plan-review`) and assume branch protection on `main` exists (so the maintainer-merge-only model holds). Codifying the repo state as a script up front means Tasks 1 and 2 don't have to babysit the labels, and re-bootstrapping a new target repo is a one-liner.

**Files (already landed in this commit, just verify):**
- `scripts/setup-target-repo.sh` — `gh`-based; idempotent; configures labels, repo settings (issues on, wiki/projects/discussions off, squash-merge, delete-branch-on-merge), branch protection on `main`.
- `config/github-app-manifest.json` — codified App permissions for Task 3.
- `docs/github-app-setup.md` — manual web steps for App registration + install (with the exact form fields).

- [ ] **Step 1: Run on the live target repo**

```bash
scripts/setup-target-repo.sh jonaseck2/slaktforskning
```

Verify:

```bash
gh label list -R jonaseck2/slaktforskning --json name --jq '[.[].name]'
gh api /repos/jonaseck2/slaktforskning/branches/main/protection \
  --jq '{required_pull_request_reviews, allow_force_pushes, allow_deletions}'
```

The script is idempotent — re-running it on a repo that already has the labels just updates the colors/descriptions to match the spec.

- [ ] **Step 2: Run on the pipeline repo itself, before going public**

```bash
scripts/setup-target-repo.sh jonaseck2/agentic-dev-pipeline
```

Same labels and protection. The pipeline repo will receive its own bug reports / feature requests; having the same triage labels keeps the model uniform.

---

## Task 1: Diff policy on the impl agent's output

**Why first:** This is the single change that most reduces real risk. Even if everything else stays as-is, a poisoned agent that can't touch `.github/workflows` or open a 5,000-line PR can't execute the supply-chain pivot.

**Files:**
- New: `operator/internal/controller/diff_policy.go`
- Modify: `operator/internal/controller/devtask_controller.go` (call the policy in the `PodSucceeded` branch, after `findPRForTask` returns the PR)
- Modify: `operator/internal/controller/pod.go` (drop the line "`.devcontainer/ and .github/workflows/ are fair game if the issue explicitly targets them`" — that's the foot-gun)
- New: `deploy/system/diff-policy.yaml` (ConfigMap with the path/size limits)
- Modify: `operator/cmd/main.go` (mount the ConfigMap as flags or env)

**Policy shape:**

```yaml
restrictedPaths:
  # Hard-block — fail the DevTask, do not require human override
  - .github/**
  - .devcontainer/**
  - Dockerfile
  - .mcp.json
  - operator/**          # the operator must not modify itself
  - deploy/**

riskyPaths:
  # Soft-block — allow only if the issue body has the literal token "approve-risky-paths: <pattern>"
  - package.json
  - package-lock.json
  - "**/Dockerfile"
  - "**/Makefile"

maxFilesChanged: 25
maxLinesChanged: 800
```

The numbers are starting points; tune from real demo data, not from gut feel.

- [ ] **Step 1: Add `gh.PullRequest.Files` lookup to `findPRForTask` callers**

The `go-github` client already returns the PR object. Adding a separate `ListFiles` call gives us the per-file additions/deletions counts and the path list. Keep it as a separate function `listPRFiles(ctx, ghClient, owner, repo, prNumber)` so the diff policy can call it without re-fetching the PR.

- [ ] **Step 2: Implement `diff_policy.go`**

Single function:

```go
type DiffViolation struct {
    Reason   string   // "restricted-path" | "risky-path" | "too-many-files" | "too-many-lines"
    Path     string   // populated for path violations, empty otherwise
    Detail   string   // human-readable, goes into DevTask status.Message
}

func evaluateDiff(files []*gh.CommitFile, issueBody string, policy DiffPolicy) []DiffViolation
```

- Restricted paths: any match → violation, regardless of issue body.
- Risky paths: match → violation unless `issueBody` contains `approve-risky-paths: <glob>` AND the matched path satisfies the glob.
- File/line caps: sum across all files; one violation per cap.

- [ ] **Step 3: Wire into `PodSucceeded`**

After `findPRForTask` returns a non-nil `pr`:
1. Fetch PR files via `listPRFiles`.
2. Read the issue body via the existing GH client.
3. Run `evaluateDiff`.
4. If violations: close the PR with a comment explaining each violation, transition DevTask to `Failed` with `status.Message = "diff policy: <first reason>"`, do NOT post the `PR:` comment on the issue.
5. If no violations: existing behavior (record PRNumber, transition to AwaitingReview, post `PR: <url>` comment).

The PR-close step is important — leaving an open rejected PR creates noise. The comment that explains the violation is what a human-ish maintainer would have written anyway.

- [ ] **Step 4: Update the agent prompt**

In `pod.go` `buildAgentPrompt`, replace:

> `.devcontainer/ and .github/workflows/ are fair game if the issue explicitly targets them`

with:

> The operator enforces a diff policy: never edit `.github/`, `.devcontainer/`, `Dockerfile`, or `.mcp.json` — the operator will reject the PR. Edits to `package.json`/`package-lock.json` need the issue body to contain `approve-risky-paths: package*.json`.

- [ ] **Step 5: Tests**

`diff_policy_test.go` with a table-driven test covering each violation type and the `approve-risky-paths` escape hatch. This is pure logic — no envtest needed; runs in plain `go test`.

- [ ] **Step 6: Demo verification**

File two demo issues:
1. One that asks for a normal docs change → should pass policy, PR opens, issue gets `PR:` comment.
2. One whose plan would touch `.github/workflows/release.yml` → should be rejected, DevTask goes to `Failed`, PR is closed with a comment naming the violated path.

---

## Task 2: Label-gated plan review for risk patterns

**Why second:** Closes the spend-before-human-sees-it gap. A maintainer reads the triage plan, decides whether the impl agent should run, and signals approval via a label.

This is intentionally low-tech — no extra Claude pass, no extra service. Just a label transition the maintainer controls.

**Files:**
- Modify: `deploy/triage/cronjob.yaml` (the triage prompt's labeling logic)
- Modify: `deploy/triage/configmap.yaml` (the prompt template)
- Modify: `operator/internal/controller/github_poller.go` (the poller already filters on `ready-for-development`; no change unless we add a new label)

**Label flow today:**
1. Human files `needs-triage`.
2. Triage agent writes plan, applies `ready-for-development`.
3. Operator picks up `ready-for-development`, spawns impl pod.

**New flow:**
1. Human files `needs-triage`.
2. Triage agent writes plan, runs the same risk-pattern check the operator runs (path globs only — no diff yet, just heuristic match against the plan body).
3. If the plan body mentions any restricted/risky path: triage applies `needs-plan-review` instead of `ready-for-development`.
4. Maintainer reads the plan comment. If they approve, they manually relabel `needs-plan-review` → `ready-for-development`. If not, they close.
5. Operator picks up `ready-for-development` exactly as today.

The poller doesn't need to know about `needs-plan-review`; it stays as a dead-end label the maintainer transitions out of by hand. This keeps the operator change minimal.

**Risk patterns** (regex against the plan body, case-insensitive):

```
\.github/
\.devcontainer/
Dockerfile
package\.json
\.mcp\.json
secret|credential|token|apikey
deploy/|operator/
```

- [ ] **Step 1: Create `needs-plan-review` label on `slaktforskning`**

```bash
gh label create needs-plan-review --color FFA500 \
  --description "Triage flagged this for human plan review before impl agent runs"
```

- [ ] **Step 2: Update the triage prompt**

In `deploy/triage/configmap.yaml`, append to the existing prompt:

> After writing the plan, scan the plan body for any of these patterns: `.github/`, `.devcontainer/`, `Dockerfile`, `package.json`, `.mcp.json`, `secret`, `credential`, `token`, `apikey`, `deploy/`, `operator/`. If ANY pattern matches, apply the label `needs-plan-review` instead of `ready-for-development`. Otherwise, apply `ready-for-development` as before.

The pattern check is done by the triage agent itself (a Claude call). It's fine that it's heuristic — the label is reversible, the maintainer is the actual gate.

- [ ] **Step 3: Document the maintainer flow**

Update `README.md` with a "Reviewing plans" section: when an issue is labeled `needs-plan-review`, read the most recent comment from `jonaseck2-bot` (or whatever the agent's GH identity is), and either relabel to `ready-for-development` to proceed or close the issue.

- [ ] **Step 4: Demo verification**

File two issues:
1. "Add a button to the homepage" → triage should label `ready-for-development`.
2. "Update the CI workflow to run on Node 22" → triage should label `needs-plan-review`. Operator must NOT spawn an impl pod. Manually relabel and confirm the impl pod then spawns.

---

## Task 3: GitHub App in place of the fine-grained PAT

**Why third:** Highest infra lift; lowest marginal risk reduction once Tasks 1 and 2 are done. But it's the right thing to do before exposing this to anyone else's repos, and it gives us per-installation rate limits + audit logs that a PAT cannot.

**Files:**
- Modify: `operator/internal/controller/github_poller.go`, `operator/internal/controller/pod.go` (token sourcing)
- New: `operator/internal/controller/github_app.go` (installation-token mint logic)
- Modify: `Makefile` (`secrets` target reads new env vars)
- Modify: `README.md` (GitHub App setup instructions)

**App permissions** (request only what's actually needed):

- Contents: Read & write (push branches)
- Issues: Read & write (read body, comment, label)
- Pull requests: Read & write (open, close, list, comment)
- Metadata: Read (required by GitHub for any installed app)

Skip: actions, packages, security events, anything else GitHub offers.

**Token shape:**

- One `installation_id` per target repo.
- Operator mints a fresh installation token per DevTask reconcile; tokens TTL is 1 hour, which is plenty for a single agent run.
- The triage CronJob mints its own short-lived token at job start.

This means the agent pod no longer holds a long-lived secret — even if the agent leaks `$GITHUB_TOKEN`, the token expires within an hour and is scoped to one installation.

- [ ] **Step 1: Register the GitHub App**

Follow [`docs/github-app-setup.md`](../github-app-setup.md) — that's the deterministic reference for the form-fill steps. Permissions are codified in [`config/github-app-manifest.json`](../../config/github-app-manifest.json); after registration, run the verify command from the docs to confirm the live App permissions match the manifest exactly. Save the App ID, the installation ID, and the private-key PEM at `~/.config/agentic-dev-pipeline/github-app.pem` (chmod 600).

- [ ] **Step 2: Add `github_app.go` with installation-token minter**

Use `github.com/bradleyfalzon/ghinstallation/v2` (already a transitive dep of `go-github`'s ecosystem). Single function `mintInstallationToken(ctx, appID, installationID, privateKeyPEM []byte) (string, error)` that returns a fresh token.

- [ ] **Step 3: Replace `creds.githubToken` reads**

Every place the operator currently calls `readPipelineCredentials(ctx, c)` and uses `creds.githubToken`, replace with a call to `mintInstallationToken`. Cache the token for ~50 minutes per (appID, installationID) tuple to avoid hitting the JWT-mint endpoint on every reconcile.

- [ ] **Step 4: Update the agent pod's env wiring**

In `pod.go`, the agent pod's `GITHUB_TOKEN` env var still pulls from a Secret — but the operator must rotate that Secret with a fresh installation token before each pod spawn. Two approaches:

- **Easier**: write the freshly-minted token into the per-task Secret in `ensureTaskSecret` (which is already created per-DevTask). Token lives only for the lifetime of that namespace, which is the lifetime of the agent run.
- **Cleaner**: make the operator project-token the agent process via downward API or an init-container that fetches a fresh token. Adds complexity; defer.

Take the easier path; document the cleaner one as a follow-up.

- [ ] **Step 5: Update the Makefile**

Replace `GITHUB_TOKEN` in `make secrets` with `GH_APP_ID`, `GH_INSTALLATION_ID`, and a path to the private key:

```makefile
secrets:
	@test -n "$(GH_APP_ID)" || (echo "GH_APP_ID not set" && exit 1)
	@test -n "$(GH_INSTALLATION_ID)" || (echo "GH_INSTALLATION_ID not set" && exit 1)
	@test -n "$(GH_APP_PRIVATE_KEY_PATH)" || (echo "GH_APP_PRIVATE_KEY_PATH not set" && exit 1)
	# ... CLAUDE_TOKEN check unchanged ...
	@kubectl create secret generic pipeline-app-key \
		--namespace devpipeline-system \
		--from-literal=app-id="$(GH_APP_ID)" \
		--from-literal=installation-id="$(GH_INSTALLATION_ID)" \
		--from-file=private-key="$(GH_APP_PRIVATE_KEY_PATH)" \
		--dry-run=client -o yaml | kubectl apply -f -
	# pipeline-creds keeps just the Claude token + git author info now
```

- [ ] **Step 6: Update README**

Add a "GitHub App setup" section. List the permissions, link to GitHub's "Register a new app" page, and document the four env vars needed for `make secrets`.

- [ ] **Step 7: Demo verification**

Run a demo end-to-end with the App-token path. Verify with `gh api /rate_limit` (using the minted token) that the auth shows the App identity, not a user PAT. Verify the per-task Secret is rotated by inspecting `kubectl get secret -n devtask-N -o yaml` between two runs and confirming the token value differs.

---

## Sequencing

Do these in order. Each task is independently shippable; don't pipeline them.

1. **Task 1** lands first. Smallest dependency surface, biggest risk reduction. Operator-only change.
2. **Task 2** lands second. Touches the triage prompt — needs a demo run to confirm the heuristic fires correctly without too many false positives.
3. **Task 3** lands last. Biggest infra lift, requires registering a GitHub App, rotates how the whole system authenticates. Don't touch this while Task 2's heuristic is still being tuned.

After all three: this is publishable. Add `LICENSE` (MIT or Apache-2.0; default to Apache-2.0 since you may want patent protection if this gets copied), `CONTRIBUTING.md`, and `SECURITY.md` (point to a contact channel for vulnerability reports). Those are paperwork, not engineering — separate small commit.

## What this plan does NOT cover

- **Cost caps.** A poisoned agent can still burn Claude tokens up to the per-task pod timeout (4 minutes today). Out of scope; revisit if abuse becomes real.
- **Author allowlist on `needs-triage`.** Today anyone with triage permission on the target repo can label. Mitigate via GitHub's repo permissions, not in this codebase.
- **Multi-repo / multi-tenancy.** Whole separate design problem. v2.0 territory.
- **Removing `--dangerously-skip-permissions`.** That flag is structural — without it, the headless agent blocks waiting for human approval. Replace only when there's a real auto-approval mechanism worth building.
