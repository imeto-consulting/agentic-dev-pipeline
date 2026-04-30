#!/usr/bin/env bash
# Configures a target repo with the labels and branch-protection settings
# the agentic dev pipeline expects. Idempotent — safe to re-run.
#
# Usage: scripts/setup-target-repo.sh OWNER/REPO
#
# Requires: gh (authenticated as a user with admin rights on the target repo).
# Read-only repo permissions are insufficient — branch protection needs admin.
set -euo pipefail

REPO="${1:?usage: $0 OWNER/REPO}"

echo "==> Verifying gh auth and admin access on $REPO"
PERMISSION=$(gh api "/repos/$REPO" --jq '.permissions.admin // false')
if [ "$PERMISSION" != "true" ]; then
  echo "ERROR: the authenticated gh user does not have admin on $REPO." >&2
  echo "       Branch protection requires admin." >&2
  exit 1
fi

echo "==> Ensuring labels"
# Label spec: name|hex-color|description. Keep colors stable so the same label
# rendering shows up across all pipeline-managed repos.
declare -a LABELS=(
  "needs-triage|c5def5|Triage agent should pick this up"
  "ready-for-development|0e8a16|Plan approved; impl agent will run on next reconcile"
  "needs-plan-review|FFA500|Triage flagged this for human plan review before impl agent runs"
  "needs-info|d4c5f9|Agent requested clarification; waiting for human reply"
)

for spec in "${LABELS[@]}"; do
  IFS='|' read -r name color desc <<<"$spec"
  if gh label list -R "$REPO" --json name --jq '.[].name' | grep -qx "$name"; then
    echo "    edit  $name"
    gh label edit "$name" -R "$REPO" --color "$color" --description "$desc" >/dev/null
  else
    echo "    create $name"
    gh label create "$name" -R "$REPO" --color "$color" --description "$desc" >/dev/null
  fi
done

echo "==> Configuring repo settings"
# Disable wiki and projects (the pipeline doesn't use them; less surface).
# Keep issues enabled — they're the input channel.
gh repo edit "$REPO" \
  --enable-issues \
  --enable-wiki=false \
  --enable-projects=false \
  --enable-discussions=false \
  --enable-squash-merge \
  --delete-branch-on-merge \
  >/dev/null

echo "==> Setting branch protection on main"
# Require: 1 approving review, dismiss stale on push, no force-push, no deletes.
# No status checks listed (yet) — add those when CI lands. Admins are NOT
# enforced so the maintainer can break-glass push if the pipeline jams.
gh api -X PUT "/repos/$REPO/branches/main/protection" \
  -H "Accept: application/vnd.github+json" \
  --input - <<'JSON' >/dev/null
{
  "required_status_checks": null,
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "required_approving_review_count": 1,
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_conversation_resolution": true,
  "lock_branch": false,
  "allow_fork_syncing": false
}
JSON

echo "==> Done. Verify with: gh api /repos/$REPO/branches/main/protection --jq '.required_pull_request_reviews'"
