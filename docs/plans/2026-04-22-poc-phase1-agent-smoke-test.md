# POC Phase 1: Agent Smoke Test

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove that `claude -p` can maintain `slaktforskning` from a prompt before building any cluster infrastructure. This is the highest-risk assumption in the whole pipeline — validate it first.

**Architecture:** No cluster, no operator, no sandbox. The agent runs locally in the slaktforskning devcontainer via VS Code Dev Containers. The GitHub MCP is added to `.mcp.json` so the agent can read issues, open PRs, and comment.

**Tech Stack:** Claude Code CLI, `@modelcontextprotocol/server-github` MCP, slaktforskning devcontainer (Node.js/TypeScript/Electron, Dockerfile-based)

---

### Task 1: Add GitHub MCP to slaktforskning

**Files:**
- Modify: `/Users/jonasahnstedt/git/slaktforskning/.mcp.json`

The existing `.mcp.json` only has the slaktforskning app MCPs. The GitHub MCP must be added so `claude -p` can read issues and open PRs.

- [ ] **Step 1: Read current .mcp.json**

```bash
cat /Users/jonasahnstedt/git/slaktforskning/.mcp.json
```

Expected output: object with `mcpServers.slaktforskning` and `mcpServers.slaktforskning-dev`.

- [ ] **Step 2: Add GitHub MCP server**

Edit `/Users/jonasahnstedt/git/slaktforskning/.mcp.json` to add the github entry alongside the existing servers:

```json
{
  "mcpServers": {
    "slaktforskning": {
      "command": "npx",
      "args": ["tsx", "src/mcp/server.ts"],
      "cwd": "."
    },
    "slaktforskning-dev": {
      "command": "npx",
      "args": ["tsx", "src/mcp/devServer.ts"],
      "cwd": "."
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"
      }
    }
  }
}
```

- [ ] **Step 3: Verify the file is valid JSON**

```bash
python3 -c "import json; json.load(open('/Users/jonasahnstedt/git/slaktforskning/.mcp.json')); print('valid JSON')"
```

Expected: `valid JSON`

- [ ] **Step 4: Commit**

```bash
cd /Users/jonasahnstedt/git/slaktforskning
git add .mcp.json
git commit -m "chore: add GitHub MCP for agentic pipeline"
```

---

### Task 2: Wire credentials into the devcontainer and verify the claude CLI

**Files:**
- Modify: `/Users/jonasahnstedt/git/slaktforskning/.devcontainer/devcontainer.json`
- Modify: `/Users/jonasahnstedt/git/slaktforskning/Dockerfile` (if claude CLI is missing)

Claude Max does not use `ANTHROPIC_API_KEY`. It authenticates via `CLAUDE_CODE_OAUTH_TOKEN`. The devcontainer already forwards this from the host (see the existing `remoteEnv` block). We need to add `GITHUB_TOKEN` the same way — passed from your host environment, never committed.

**Do not hardcode tokens anywhere.** Set them once in your macOS environment and the devcontainer picks them up automatically.

- [ ] **Step 1: Set host environment variables (one-time setup)**

Add to `~/.zshrc` (or run via `launchctl setenv` for persistence across VS Code restarts):

```bash
# Add to ~/.zshrc
export GITHUB_TOKEN="<your-fine-grained-PAT>"
export CLAUDE_CODE_OAUTH_TOKEN="<your-Claude-Max-OAuth-token>"
```

Then reload: `source ~/.zshrc`

To get your `CLAUDE_CODE_OAUTH_TOKEN`: run `claude` in a terminal on your host — it will prompt you to log in. After login the token is stored in `~/.claude/.credentials.json`. Extract it:

```bash
python3 -c "import json; d=json.load(open(open.path.expanduser('~/.claude/.credentials.json'))); print(d.get('oauthToken',''))"
```

The fine-grained PAT needs: Contents Read+Write, Issues Read+Write, Pull Requests Read+Write, scoped to `jonaseck2/slaktforskning`.

- [ ] **Step 2: Add GITHUB_TOKEN to the devcontainer remoteEnv**

The existing `devcontainer.json` already forwards `CLAUDE_CODE_OAUTH_TOKEN` from the host. Add `GITHUB_TOKEN` alongside it.

Edit `/Users/jonasahnstedt/git/slaktforskning/.devcontainer/devcontainer.json`:

Find the `remoteEnv` block:

```json
"remoteEnv": {
    "DISPLAY": ":99",
    "CLAUDE_CODE_OAUTH_TOKEN": "${localEnv:CLAUDE_CODE_OAUTH_TOKEN}"
}
```

Add `GITHUB_TOKEN`:

```json
"remoteEnv": {
    "DISPLAY": ":99",
    "CLAUDE_CODE_OAUTH_TOKEN": "${localEnv:CLAUDE_CODE_OAUTH_TOKEN}",
    "GITHUB_TOKEN": "${localEnv:GITHUB_TOKEN}"
}
```

- [ ] **Step 3: Check if Dockerfile installs claude CLI**

```bash
grep -n "claude\|anthropic" /Users/jonasahnstedt/git/slaktforskning/Dockerfile
```

If a `npm install -g @anthropic-ai/claude-code` line is present, skip to Step 5.

- [ ] **Step 4: If claude CLI is missing, add it to the Dockerfile**

Locate the section after `npm install` or at the end of the build steps, before the `USER` instruction, and add:

```dockerfile
RUN npm install -g @anthropic-ai/claude-code
```

- [ ] **Step 5: Rebuild the devcontainer**

In VS Code: Cmd+Shift+P → "Dev Containers: Rebuild Container"

Then in the container terminal verify:

```bash
claude --version             # expected: 1.x.x
echo "${GITHUB_TOKEN}" | head -c 10  # expected: first 10 chars of your PAT (not empty)
echo "${CLAUDE_CODE_OAUTH_TOKEN}" | head -c 10  # expected: first 10 chars (not empty)
```

If either token is empty, your host env var is not set — set it and rebuild.

- [ ] **Step 6: Confirm GitHub MCP is discoverable**

```bash
# Inside the devcontainer
claude mcp list
```

Expected: `github` appears in the list alongside `slaktforskning` and `slaktforskning-dev`.

- [ ] **Step 7: Commit changes**

```bash
cd /Users/jonasahnstedt/git/slaktforskning
git add .devcontainer/devcontainer.json Dockerfile
git commit -m "chore: pass GITHUB_TOKEN via remoteEnv, install claude CLI"
```

---

### Task 3: File a test issue on slaktforskning

**Files:** None (GitHub issue creation)

File a real, well-scoped issue on `jonaseck2/slaktforskning` that serves as the first pipeline test. The issue body must contain a complete implementation plan, since in Phase 1 we're writing the plan ourselves (the triage agent comes in Phase 4).

- [ ] **Step 1: Choose a well-scoped change**

Good candidates (small, bounded, verifiable):
- "Add `--limit N` flag to list-persons command that caps the number of results returned"
- "Show birth year in person search results alongside name"
- "Add input validation: reject negative birth years"

Pick whichever makes most sense given the current codebase state.

- [ ] **Step 2: Read relevant source files to write a real plan**

```bash
ls /Users/jonasahnstedt/git/slaktforskning/src/
```

Find the file(s) that need changing for your chosen issue. Read them. Write a plan that references real file paths and real function names.

- [ ] **Step 3: Create the issue on GitHub**

```bash
gh issue create \
  --repo jonaseck2/slaktforskning \
  --title "<your issue title>" \
  --body "$(cat << 'ISSUE_BODY'
## Description
<one paragraph describing the feature or bug>

## Implementation Plan

**Files to change:**
- `src/<actual-file>.ts` — <what to do>

**Steps:**
1. <specific step with file path>
2. <specific step>
3. Run tests: `npm test`
4. Verify: <how to confirm it works>

**Acceptance criteria:**
- <specific, testable criterion>
ISSUE_BODY
)"
```

Note the issue number from the output.

- [ ] **Step 4: Add the `needs-triage` label**

```bash
gh issue edit <NUMBER> --repo jonaseck2/slaktforskning --add-label "needs-triage"
```

If the label doesn't exist:

```bash
gh label create "needs-triage" --repo jonaseck2/slaktforskning --color "FFA500"
gh label create "ready-for-development" --repo jonaseck2/slaktforskning --color "0075ca"
gh label create "needs-info" --repo jonaseck2/slaktforskning --color "e4e669"
```

---

### Task 4: Run claude -p against the issue

**Files:** None (running the agent)

This is the core validation. We run the agent locally inside the devcontainer and observe whether it produces a working PR.

- [ ] **Step 1: Verify environment variables are available**

Inside the devcontainer (these come from `remoteEnv` → `localEnv` — no manual export needed):

```bash
echo "GITHUB_TOKEN set: $([ -n "${GITHUB_TOKEN}" ] && echo yes || echo NO)"
echo "CLAUDE_CODE_OAUTH_TOKEN set: $([ -n "${CLAUDE_CODE_OAUTH_TOKEN}" ] && echo yes || echo NO)"
```

Both should say `yes`. If not, see Task 2 Step 1 about setting host environment variables.

- [ ] **Step 2: Navigate to the repo root**

```bash
cd /workspaces/slaktforskning
```

The working directory matters: `claude -p` reads `.mcp.json` from the working directory.

- [ ] **Step 3: Run the agent**

```bash
claude -p "You are working on GitHub issue #<NUMBER> in jonaseck2/slaktforskning.

Your task:
1. Read the issue and any comments using the GitHub MCP (mcp__github). The issue body contains an implementation plan — follow it.
2. Implement the changes on a branch named claude/issue-<NUMBER>.
3. Run the tests: npm test. If they fail, iterate until they pass.
4. Commit and push. Open a PR against main linking the issue.
5. Post a comment on the issue with the PR URL.

If the plan is unclear or you hit an unrecoverable blocker:
- Commit any work-in-progress to the branch and push
- Open a draft PR
- Comment on the issue starting with '/clarification:' and explain what you need
- Exit with code 2

Do not touch .devcontainer/devcontainer.json, .mcp.json, or .github/workflows/ unless the issue specifically asks." \
  --allowedTools "Read,Edit,Write,Bash,mcp__github" \
  --dangerously-skip-permissions \
  --output-format json \
  2>&1 | tee /tmp/claude-run-1.json
```

- [ ] **Step 4: Inspect the output**

```bash
# Check exit code (0 = success, 2 = clarification needed, other = failure)
echo "Exit code: $?"

# Inspect JSON output
python3 -c "
import json, sys
data = json.load(open('/tmp/claude-run-1.json'))
print('Result:', str(data.get('result', ''))[:500])
# Note: Claude Max via OAuth does not return cost.total_cost
" 2>/dev/null || cat /tmp/claude-run-1.json | tail -5

# Check GitHub for the PR
gh pr list --repo jonaseck2/slaktforskning --state open
```

Expected: A PR is open on `jonaseck2/slaktforskning` with a branch named `claude/issue-<NUMBER>`.

- [ ] **Step 5: Review the PR**

```bash
gh pr view <PR-NUMBER> --repo jonaseck2/slaktforskning --web
```

Evaluate:
- Does the diff match the implementation plan?
- Do the tests pass (check CI)?
- Is the commit message sensible?
- Did the agent comment on the issue with the PR URL?

---

### Task 5: Iterate on 2 more issue shapes

**Files:** None (prompt iteration)

The goal is 3 successful issue types: bug fix, small feature, docs update. This validates the prompt works across different work shapes.

- [ ] **Step 1: File a bug-fix issue**

Create an issue describing a real bug in `slaktforskning` (or a synthetic one if no real bugs present). Include a specific repro step and expected vs. actual behavior. Write the implementation plan in the body. Run the agent as in Task 4, Step 3.

- [ ] **Step 2: File a docs issue**

Create an issue asking to add or update documentation (e.g., "Add JSDoc to the MCP server's `search_persons` tool describing parameters and return format"). Write the plan in the body. Run the agent.

- [ ] **Step 3: Evaluate results across all 3 runs**

For each run, record:
- Did it open a PR? Y/N
- Did tests pass? Y/N
- Did it stay on scope? Y/N
- What went wrong (if anything)?

If the agent fails on any shape, adjust the prompt (the template at Task 4 Step 3) and re-run. Document what changed and why.

- [ ] **Step 4: Note the prompt version that works**

Save the working prompt to a file in this repo:

```bash
mkdir -p /Users/jonasahnstedt/git/agentic-dev-pipeline/config
cat << 'PROMPT' > /Users/jonasahnstedt/git/agentic-dev-pipeline/config/agent-prompt-v1.txt
You are working on GitHub issue #{issueNumber} in {repo}.

Your task:
1. Read the issue and any comments using the GitHub MCP (mcp__github). The issue body contains an implementation plan — follow it.
2. Implement the changes on a branch named claude/issue-{issueNumber}.
3. Run the tests. If they fail, iterate until they pass.
4. Commit and push. Open a PR against main linking the issue.
5. Post a comment on the issue with the PR URL.

If the plan is unclear or you hit an unrecoverable blocker:
- Commit any work-in-progress to the branch and push
- Open a draft PR
- Comment on the issue starting with "/clarification:" and explain what you need
- Exit with code 2

Do not touch .devcontainer/devcontainer.json, .mcp.json, or .github/workflows/ unless the issue specifically asks.
PROMPT
```

- [ ] **Step 5: Commit the prompt template**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add config/agent-prompt-v1.txt
git commit -m "chore: working agent prompt template v1"
```

---

## Exit Criteria

Phase 1 is complete when:

1. `claude -p` reliably implements small issues on `slaktforskning`, opening a real PR
2. It works across at least 3 issue shapes (feature, bug fix, docs)
3. The working prompt template is saved to `config/agent-prompt-v1.txt`
4. Labels `needs-triage`, `ready-for-development`, and `needs-info` exist on the `slaktforskning` repo

Move this plan to `docs/plans/archive/` when done.
