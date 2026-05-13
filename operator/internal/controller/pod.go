/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const agentPodName = "agent"

// agentImage is the pre-built devcontainer image the agent pod runs. We use it directly
// rather than running envbuilder on every task start: envbuilder's postCreateCommand
// (npm install + Playwright browser download) adds ~600 MiB of downloads and consistently
// OOMKills the pod. The cached image already has claude, git, gh, and all system packages
// installed. In-cluster registry uses internal port 5000; host-side is localhost:5050.
//
// Configured via the AGENT_IMAGE env var (set by `make run` from .pipeline.env).
func agentImage() string {
	if v := os.Getenv("AGENT_IMAGE"); v != "" {
		return v
	}
	return "localhost:5000/devcontainer:latest"
}


func int64Ptr(i int64) *int64 { return &i }
func boolPtr(b bool) *bool    { return &b }

func repoName(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return repo
}

func buildAgentPrompt(task *devpipelinev1alpha1.DevTask) string {
	return fmt.Sprintf(
		"You are working on GitHub issue #%d in %s.\n\n"+
			"Steps (in order):\n"+
			"1. Read the issue: `gh issue view %d -R %s`\n"+
			"2. The wrapper script may have already checked out an existing branch for this issue. Verify with `git branch --show-current`. If the output starts with `claude/issue-%d`, you are set — proceed to step 3. Otherwise create a new branch with a slug from the issue title on a SINGLE LINE: `SLUG=$(gh issue view %d -R %s --json title --jq '.title | ascii_downcase | gsub(\"[^a-z0-9]+\"; \"-\") | ltrimstr(\"-\") | rtrimstr(\"-\") | .[0:40]') && git checkout -b \"claude/issue-%d-${SLUG}\"`\n"+
			"3. Implement the fix described in the issue body. Make ALL file changes now.\n"+
			"4. Stage (restore pipeline-internal files first so they are not committed as deleted):\n"+
			"   `git restore .mcp.json 2>/dev/null || true && git add -A`\n"+
			"5. Commit with Signed-off-by, using SEPARATE -m flags on a SINGLE LINE — each -m becomes its own paragraph. The first -m is the PR title; the rest become the PR body:\n"+
			"   `git commit -s -m \"fix: <one-line description of what you changed>\" -m \"Closes #%d\" -m \"Changes: <what changed and why, one short sentence>\" -m \"Test plan: <what to verify>\"`\n"+
			"6. Push: `git push -u origin HEAD`\n"+
			"7. Create PR — use --fill-first so the PR title/body come from the commit message you just made. Run on a single line:\n"+
			"   `gh pr create --base main --fill-first`\n"+
			"   You're done after this step. The operator detects the PR by branch name and posts the PR URL on the issue itself — do NOT comment on the issue from the agent.\n\n"+
			"CRITICAL bash invariants — break these and the run fails:\n"+
			"- Every Bash command MUST fit on a SINGLE LINE. The headless bash wrapper inserts a literal '\\n' arg at every line continuation, so multi-line commands corrupt into things like `git commit -s \\n -m ...` where `\\n` becomes a pathspec/positional arg.\n"+
			"- NEVER use heredocs (`cat <<EOF`), backslash-newline continuations, or multi-line `--body \"...\"`. Use multiple -m flags for the commit body and `gh pr create --fill-first` for the PR body.\n\n"+
			"Rules:\n"+
			"- NEVER use placeholder text like '<description>' or '<url>' — always use real values\n"+
			"- ALWAYS run git restore .mcp.json before git add -A\n"+
			"- NEVER create a PR before committing\n"+
			"- NEVER comment on the issue from the agent — the operator does that\n"+
			"- If tests are relevant, run them after committing (step 5.5): push anyway if minor failures\n"+
			"- If blocked: commit WIP, push, open draft PR with --draft, comment '/clarification:' on issue\n"+
			"- .devcontainer/ and .github/workflows/ are fair game if the issue explicitly targets them\n"+
			"- Use Bash for all git/gh commands. GITHUB_TOKEN is pre-set.",
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.IssueNumber, task.Spec.Repo, task.Spec.IssueNumber,
		task.Spec.IssueNumber,
	)
}

func secretRef(task *devpipelinev1alpha1.DevTask, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: taskSecretName(task)},
			Key:                  key,
		},
	}
}

func agentPod(task *devpipelinev1alpha1.DevTask, githubToken, claudeToken string) *corev1.Pod {
	ns := taskNamespace(task)
	repo := repoName(task.Spec.Repo)
	prompt := buildAgentPrompt(task)

	// Clone the repo and run claude as the node user (UID 1000).
	// Credentials are stored via git-credentials file so the remote URL stays clean
	// and git push / gh pr create work without exposing the token in git remote -v output.
	runScript := fmt.Sprintf(
		"#!/bin/bash\nset -e\n"+
			"export HOME=/home/node\n"+
			// In oauth mode, unset ANTHROPIC_API_KEY so Claude Code uses CLAUDE_CODE_OAUTH_TOKEN
			// (subscription billing) instead of API billing. Both env vars are sourced from the
			// same secret key; the auth mode flag determines which one Claude Code reads.
			"[ \"${CLAUDE_AUTH_MODE}\" = \"oauth\" ] && unset ANTHROPIC_API_KEY || true\n"+
			// Set up git credential store so push works without token in the remote URL.
			// echo expands ${GITHUB_PERSONAL_ACCESS_TOKEN} from the container environment at runtime.
			"git config --global credential.helper store\n"+
			"echo \"https://x-access-token:${GITHUB_PERSONAL_ACCESS_TOKEN}@github.com\" > /home/node/.git-credentials\n"+
			"git config --global --add safe.directory /workspaces/%s\n"+
			"git config --global user.name \"${GIT_AUTHOR_NAME}\"\n"+
			"git config --global user.email \"${GIT_AUTHOR_EMAIL}\"\n"+
			"git clone https://github.com/%s /workspaces/%s\n"+
			"cd /workspaces/%s\n"+
			// Pre-checkout: if a branch matching `claude/issue-N` (legacy) or `claude/issue-N-*` (slug)
			// already exists on the remote, check it out so the agent continues on the same branch.
			// Protects against creating a duplicate branch when the issue title (slug) changes.
			"EXISTING_BRANCH=$(git ls-remote --heads origin \"claude/issue-%d\" \"claude/issue-%d-*\" 2>/dev/null | head -1 | awk '{print $2}' | sed 's|refs/heads/||'); [ -n \"$EXISTING_BRANCH\" ] && git checkout \"$EXISTING_BRANCH\" || true\n"+
			// Remove .mcp.json so claude does not try to spawn Node.js MCP servers,
			// which get OOMKilled due to Docker VM swap exhaustion. gh CLI covers all
			// GitHub operations we need (gh issue view, gh pr create, gh issue comment).
			"rm -f .mcp.json\n"+
			"claude -p %q "+
			"--allowedTools 'Read,Edit,Write,Bash' "+
			"--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
		repo, task.Spec.Repo, repo, repo,
		task.Spec.IssueNumber, task.Spec.IssueNumber,
		prompt,
	)

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentPodName,
			Namespace: ns,
			Labels:    map[string]string{"devpipeline.local/task": task.Name},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: int64Ptr(1800),
			// Run everything as the node user (UID/GID 1000) so claude's
			// --dangerously-skip-permissions flag is accepted (it refuses root).
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:    int64Ptr(1000),
				RunAsGroup:   int64Ptr(1000),
				FSGroup:      int64Ptr(1000),
				RunAsNonRoot: boolPtr(true),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			InitContainers: []corev1.Container{{
				Name:    "write-script",
				Image:   "busybox",
				Command: []string{"sh", "-c", "printf '%s' \"$SCRIPT\" > /tmp/run-agent.sh && chmod +x /tmp/run-agent.sh"},
				Env:     []corev1.EnvVar{{Name: "SCRIPT", Value: runScript}},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: boolPtr(false),
					ReadOnlyRootFilesystem:   boolPtr(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "tmp", MountPath: "/tmp"},
				},
			}},
			Containers: []corev1.Container{{
				Name:    "agent",
				Image:   agentImage(),
				Command: []string{"/bin/bash", "/tmp/run-agent.sh"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: boolPtr(false),
					ReadOnlyRootFilesystem:   boolPtr(true),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
				Env: []corev1.EnvVar{
					{Name: "GITHUB_PERSONAL_ACCESS_TOKEN", ValueFrom: secretRef(task, "github-token")},
					{Name: "GITHUB_TOKEN", ValueFrom: secretRef(task, "github-token")},
					{Name: "CLAUDE_CODE_OAUTH_TOKEN", ValueFrom: secretRef(task, "claude-token")},
					// Both ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN are populated from the same
					// secret value. The run script unsets ANTHROPIC_API_KEY when CLAUDE_AUTH_MODE=oauth
					// so Claude Code falls through to CLAUDE_CODE_OAUTH_TOKEN (subscription billing).
					{Name: "ANTHROPIC_API_KEY", ValueFrom: secretRef(task, "claude-token")},
					{Name: "CLAUDE_AUTH_MODE", ValueFrom: secretRef(task, "claude-auth-mode")},
					{Name: "GIT_AUTHOR_NAME", ValueFrom: secretRef(task, "git-author-name")},
					{Name: "GIT_AUTHOR_EMAIL", ValueFrom: secretRef(task, "git-author-email")},
					{Name: "GIT_COMMITTER_NAME", ValueFrom: secretRef(task, "git-author-name")},
					{Name: "GIT_COMMITTER_EMAIL", ValueFrom: secretRef(task, "git-author-email")},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "workdir", MountPath: "/workspaces"},
					{Name: "tmp", MountPath: "/tmp"},
					{Name: "home", MountPath: "/home/node"},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "workdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func agentPodResume(task *devpipelinev1alpha1.DevTask) *corev1.Pod {
	repo := repoName(task.Spec.Repo)
	resumePrompt := fmt.Sprintf(
		"You are resuming work on GitHub issue #%d in %s.\n\n"+
			"The branch claude/issue-%d already exists on the remote. After cloning, check it out:\n"+
			"`git checkout claude/issue-%d`\n\n"+
			"Steps:\n"+
			"1. Read the latest issue comments: `gh issue view %d -R %s`\n"+
			"2. The last comment is a human answer to your /clarification request. Use that to continue.\n"+
			"3. Make all remaining file changes.\n"+
			"4. Stage (restore pipeline-internal files first so they are not committed as deleted):\n"+
			"   `git restore .mcp.json 2>/dev/null || true && git add -A`\n"+
			"5. Commit: `git commit -s -m \"fix: <one-line description>\"`\n"+
			"6. Push: `git push -u origin HEAD`\n"+
			"7. If the PR is not yet open:\n"+
			"   `PR_URL=$(gh pr create --base main \\\n"+
			"     --title \"fix: <one-line description>\" \\\n"+
			"     --body \"## Summary\\n\\nCloses #%d\\n\\n## Changes\\n\\n- <what changed and why>\\n\\n## Test plan\\n\\n- [ ] Existing tests pass\") \\\n"+
			"   && echo \"PR: $PR_URL\"`\n"+
			"   If the PR already exists, capture its URL: `PR_URL=$(gh pr view --json url --jq .url)`\n"+
			"8. Comment PR URL on issue (skip if already commented):\n"+
			"   `gh issue view %d -R %s --json comments --jq '.[].body' | grep -qF 'PR: http' \\\n"+
			"   || gh issue comment %d -R %s --body \"PR: $PR_URL\"`\n\n"+
			"Rules:\n"+
			"- NEVER use placeholder text like '<description>' or '<url>' — always use real values\n"+
			"- ALWAYS run git restore .mcp.json before git add -A\n"+
			"- Use Bash for all git/gh commands. GITHUB_TOKEN is pre-set.",
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.Repo,
	)

	runScript := fmt.Sprintf(
		"#!/bin/bash\nset -e\n"+
			"export HOME=/home/node\n"+
			"git config --global credential.helper store\n"+
			"echo \"https://x-access-token:${GITHUB_PERSONAL_ACCESS_TOKEN}@github.com\" > /home/node/.git-credentials\n"+
			"git config --global --add safe.directory /workspaces/%s\n"+
			"git config --global user.name \"${GIT_AUTHOR_NAME}\"\n"+
			"git config --global user.email \"${GIT_AUTHOR_EMAIL}\"\n"+
			"git clone https://github.com/%s /workspaces/%s\n"+
			"cd /workspaces/%s\n"+
			"git checkout claude/issue-%d\n"+
			"rm -f .mcp.json\n"+
			"claude -p %q "+
			"--allowedTools 'Read,Edit,Write,Bash' "+
			"--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
		repo, task.Spec.Repo, repo, repo,
		task.Spec.IssueNumber,
		resumePrompt,
	)

	pod := agentPod(task, "", "")
	pod.Spec.InitContainers[0].Env[0].Value = runScript
	return pod
}

func buildRevisionPrompt(task *devpipelinev1alpha1.DevTask) string {
	return fmt.Sprintf(
		"You are addressing PR review feedback on PR #%d for issue #%d in %s.\n\n"+
			"Steps (in order):\n"+
			"1. Read the PR review comments on a SINGLE LINE: `gh pr view %d -R %s --json reviews,comments --jq '{reviews: [.reviews[] | {author: .author.login, body: .body, state: .state}], comments: [.comments[] | {author: .author.login, body: .body}]}'`\n"+
			"2. Check out the existing branch and pull latest — run on a SINGLE LINE: `git checkout claude/issue-%d && git pull origin claude/issue-%d`\n"+
			"3. Address ALL feedback. Make every requested change now.\n"+
			"4. Stage: `git restore .mcp.json 2>/dev/null || true && git add -A`\n"+
			"5. Commit on a SINGLE LINE: `git commit -s -m \"fix: address review feedback\" -m \"Refs #%d\" -m \"Changes: <one sentence describing what you changed>\"`\n"+
			"6. Push on a SINGLE LINE: `git push --force-with-lease -u origin claude/issue-%d`\n"+
			"   The existing PR auto-updates — do NOT open a new PR.\n\n"+
			"CRITICAL bash invariants — break these and the run fails:\n"+
			"- Every Bash command MUST fit on a SINGLE LINE.\n"+
			"- NEVER use heredocs, backslash-newline continuations, or multi-line --body.\n\n"+
			"Rules:\n"+
			"- NEVER open a new PR\n"+
			"- NEVER comment on the issue or PR — the operator handles that\n"+
			"- NEVER use placeholder text like '<description>'\n"+
			"- ALWAYS run git restore .mcp.json before git add -A\n"+
			"- Use Bash for all git/gh commands. GITHUB_TOKEN is pre-set.",
		task.Status.PRNumber, task.Spec.IssueNumber, task.Spec.Repo,
		task.Status.PRNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.IssueNumber,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber,
	)
}

func agentPodRevision(task *devpipelinev1alpha1.DevTask) *corev1.Pod {
	repo := repoName(task.Spec.Repo)
	prompt := buildRevisionPrompt(task)
	runScript := fmt.Sprintf(
		"#!/bin/bash\nset -e\n"+
			"export HOME=/home/node\n"+
			"git config --global credential.helper store\n"+
			"echo \"https://x-access-token:${GITHUB_PERSONAL_ACCESS_TOKEN}@github.com\" > /home/node/.git-credentials\n"+
			"git config --global --add safe.directory /workspaces/%s\n"+
			"git config --global user.name \"${GIT_AUTHOR_NAME}\"\n"+
			"git config --global user.email \"${GIT_AUTHOR_EMAIL}\"\n"+
			"git clone https://github.com/%s /workspaces/%s\n"+
			"cd /workspaces/%s\n"+
			"git checkout claude/issue-%d\n"+
			"rm -f .mcp.json\n"+
			"claude -p %q "+
			"--allowedTools 'Read,Edit,Write,Bash' "+
			"--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
		repo, task.Spec.Repo, repo, repo,
		task.Spec.IssueNumber,
		prompt,
	)
	pod := agentPod(task, "", "")
	pod.Name = "agent-rev"
	pod.Spec.InitContainers[0].Env[0].Value = runScript
	return pod
}

func ensurePod(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	return client.IgnoreAlreadyExists(c.Create(ctx, pod))
}

func getPod(ctx context.Context, c client.Client, ns string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	// Prefer agent-rev: revision runs use this name and it is the active pod when present.
	// agent-rev is deleted before each new revision cycle, so if it exists it is the current run.
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "agent-rev"}, pod); err == nil {
		return pod, nil
	}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentPodName}, pod)
	return pod, err
}
