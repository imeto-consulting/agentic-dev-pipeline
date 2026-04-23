# Agentic Development Pipeline — Roadmap

> Milestone map. Active plans are in `docs/plans/`; finished plans move to `docs/plans/archive/`.

---

## POC: Claude as Maintainer of `slaktforskning`

**Goal:** End-to-end agentic pipeline on a laptop k3d cluster. File an issue → triage writes a plan → agent implements it → PR opens → merge → namespace cleaned up. No manual steps between filing and reviewing.

---

### Phase 1 — Agent Smoke Test ✅
**Plan:** `docs/plans/archive/2026-04-22-poc-phase1-agent-smoke-test.md`

- [x] Add GitHub MCP to `.mcp.json` in `slaktforskning`
- [x] Forward `GITHUB_PERSONAL_ACCESS_TOKEN` and `CLAUDE_CODE_OAUTH_TOKEN` via `devcontainer.json` `remoteEnv`
- [x] Remove `claude-code-action` GitHub Actions workflow (replaced by this pipeline)
- [x] Create pipeline labels on `slaktforskning`: `needs-triage`, `ready-for-development`, `needs-info`
- [x] File issue #10 with full implementation plan (add `limit` param to `search_persons`)
- [x] `claude -p` implemented issue #10 in 28 turns, 1843 tests pass, PR #11 opened
- [x] Prompt template saved to `config/agent-prompt-v1.txt`
- [x] `git commit --signoff` (`-s`) added to all prompt templates for DCO compliance

**What we learned:**
- Run `claude -p` from the host (not the devcontainer) using `GITHUB_PERSONAL_ACCESS_TOKEN=$(gh auth token)`
- Use `GITHUB_PERSONAL_ACCESS_TOKEN` as the env var name everywhere — no mapping needed
- `--allowedTools "Read,Edit,Write,Bash,mcp__github"` correctly restricts the agent; slaktforskning MCPs in `.mcp.json` are harmless (ignored via allowedTools)
- Cost: $0.86 per issue (28 turns, Sonnet 4.6)

---

### Phase 2 — k3d Cluster, CRD, Minimal Operator ✅
**Plan:** `docs/plans/archive/2026-04-22-poc-phase2-k3d-operator.md`

- [x] `k3d cluster create` with local registry and Calico
- [x] Kubebuilder scaffold: `DevTask` CRD + controller
- [x] Envbuilder builds `slaktforskning` devcontainer, caches to local registry
- [x] Configure git identity in agent pod (`GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL`) so `git commit -s` produces a valid `Signed-off-by:` line for DCO
- [x] `kubectl apply -f devtask-sample.yaml` → PR appears on `slaktforskning`

**Exit criteria:** `kubectl apply` triggers a real PR within ~5 minutes. DCO check passes on the PR. ✅ (PR #19 — jonaseck2/slaktforskning)

**What we learned:**
- Use the cached devcontainer image directly (skip envbuilder lifecycle); postCreateCommand runs on every container start and OOMKills
- Replace GitHub MCP server (`npx @modelcontextprotocol/server-github`) with `gh` CLI — MCP spawns Node.js, which gets OOMKilled under memory pressure
- Docker VM needs ≥ 16 GB RAM; with 8 GB, image page cache + k3s + claude = OOM
- No memory `Limits` on the agent pod — cgroup limit fires before system OOM when image layers fill page cache; request-only (`1Gi`) is sufficient
- Prompt must have `git add -A` explicit before `git commit`, and implementation before any push

> **Note:** DCO requires only `Signed-off-by:` in the commit message (no GPG key needed). The `-s` flag on `git commit` generates this line using `git config user.name` / `user.email`, which must be set in the pod environment.

---

### Phase 3 — Sandbox Hardening and Automated Trigger ✅
**Plan:** `docs/plans/archive/2026-04-22-poc-phase3-sandbox-hardening.md`

- [x] NetworkPolicy: deny-all + allowlist (DNS + HTTPS) per task namespace (Calico)
- [x] Per-task Kubernetes Secrets: credentials copied from `pipeline-creds` into task namespace
- [x] Pod security: non-root (UID 1000), read-only rootFS, drop ALL caps, seccomp RuntimeDefault
- [x] GitHub poller auto-creates `DevTask` CRs on `ready-for-development` label (30s poll)
- [x] Full state machine: Building→Running→AwaitingReview→Completed, BlockedOnClarification

**Exit criteria:** Label an issue → wait → PR appears. No manual CR creation.

---

### Phase 4 — Triage Agent, Packaging, Demo
**Plan:** `docs/plans/2026-04-22-poc-phase4-triage-demo.md`

- [ ] Triage CronJob (every 5 min): writes plan + applies `ready-for-development`
- [ ] Kustomization for single-command install
- [ ] `make demo` exercises the full loop end-to-end

**Exit criteria:** File an issue → triage → implementation → PR → merge → namespace cleaned up.

---

## v2.0 — Scaled Multi-Repo Pipeline

Deferred until POC is solid. See `docs/plans/design/agentic-dev-pipeline-design.md`.

Key additions over POC: repo topic opt-in, GitHub App auth, L7 egress proxy, Prometheus metrics, gVisor runtime.

---

## v2.1 — Skills Update Loop

Agent proposes a second PR against `skills/` after each task. Requires eval infrastructure to prevent skills rot.
