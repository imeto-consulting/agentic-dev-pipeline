# POC Demo Results

Date: 2026-04-23

## Issue: Add birth-year range filter to search_persons
- Issue filed: #20 (jonaseck2/slaktforskning)
- Triage: 5-minute CronJob posted an implementation plan comment and applied ready-for-development
- Implementation: PR #21 opened on claude/issue-20 within ~3 minutes of label detection
- PR title: "fix: <description>" (placeholder not substituted — prompt improvement needed)
- DevTask phase: AwaitingReview (will transition to Completed when PR is merged or closed)

## What worked
- GitHub poller (30s interval) auto-created DevTask within 30s of label being applied
- Full state machine: (empty) → Building → Running → AwaitingReview
- Triage CronJob: read issue, wrote concrete implementation plan, applied ready-for-development label
- NetworkPolicy: deny-all + DNS/HTTPS allowlist enforced by Calico
- Per-task Secrets: credentials copied from pipeline-creds into task namespace
- Pod security: non-root UID 1000, readOnlyRootFilesystem, drop ALL caps, seccomp

## What needed adjustment
- ctrl.SetupSignalHandler() called twice (panic on operator start) — fixed
- /home/node must be mounted as emptyDir (not just ~/.claude): claude writes to ~/.npm, ~/.config
  etc. at startup; readOnlyRootFilesystem with only ~/.claude mounted caused silent exit-0
- --dangerously-skip-permissions causes claude to execute tool calls but produce no JSON/text output;
  removed from triage CronJob (--allowedTools Bash is sufficient for gh CLI calls)
- isPRMergedOrClosed: added branch-name fallback when PRNumber not recorded in status

## Known issues
- PR title uses literal "<description>" placeholder instead of actual description
  → needs prompt improvement in pod.go buildAgentPrompt
- Triage comment says "PR: <url>" instead of actual PR URL (agent used placeholder)
- Agent pod succeeded (PR created) but issue comment was not properly updated
