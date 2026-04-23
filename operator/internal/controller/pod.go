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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const (
	// Use the pre-built devcontainer image directly rather than running envbuilder on every
	// task start. Envbuilder's postCreateCommand (npm install + Playwright browser download)
	// adds ~600 MiB of downloads and consistently OOMKills the pod. The cached image already
	// has claude, git, gh, and all system packages installed.
	// In-cluster registry (internal port 5000); host-side is localhost:5050.
	agentImage   = "slaktforskning-registry:5000/slaktforskning-devcontainer:latest"
	agentPodName = "agent"
	registryBase = "slaktforskning-registry:5000"
)

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
			"2. Create or check out branch: `git checkout -b claude/issue-%d 2>/dev/null || git checkout claude/issue-%d`\n"+
			"3. Implement the fix described in the issue body. Make ALL file changes now.\n"+
			"4. Stage (restore pipeline-internal files first so they are not committed as deleted):\n"+
			"   `git restore .mcp.json 2>/dev/null || true && git add -A`\n"+
			"5. Commit with Signed-off-by: `git commit -s -m \"fix: <one-line description of what you changed>\"`\n"+
			"6. Push: `git push -u origin claude/issue-%d`\n"+
			"7. Create PR (CAPTURE the URL — do not use a placeholder):\n"+
			"   `PR_URL=$(gh pr create --base main \\\n"+
			"     --title \"fix: <one-line description>\" \\\n"+
			"     --body \"## Summary\\n\\nCloses #%d\\n\\n## Changes\\n\\n- <what changed and why>\\n\\n## Test plan\\n\\n- [ ] Existing tests pass\") \\\n"+
			"   && echo \"PR: $PR_URL\"`\n"+
			"8. Comment PR URL on issue (skip if already commented):\n"+
			"   `gh issue view %d -R %s --json comments --jq '.[].body' | grep -qF 'PR: http' \\\n"+
			"   || gh issue comment %d -R %s --body \"PR: $PR_URL\"`\n\n"+
			"Rules:\n"+
			"- NEVER use placeholder text like '<description>' or '<url>' — always use real values\n"+
			"- ALWAYS run git restore .mcp.json before git add -A\n"+
			"- NEVER create a PR before committing\n"+
			"- If tests are relevant, run them after committing (step 5.5): push anyway if minor failures\n"+
			"- If blocked: commit WIP, push, open draft PR with --draft, comment '/clarification:' on issue\n"+
			"- .devcontainer/ and .github/workflows/ are fair game if the issue explicitly targets them\n"+
			"- Use Bash for all git/gh commands. GITHUB_TOKEN is pre-set.",
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.IssueNumber,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber,
		task.Spec.IssueNumber, task.Spec.Repo,
		task.Spec.IssueNumber, task.Spec.Repo,
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
			// Set up git credential store so push works without token in the remote URL.
			// echo expands ${GITHUB_PERSONAL_ACCESS_TOKEN} from the container environment at runtime.
			"git config --global credential.helper store\n"+
			"echo \"https://x-access-token:${GITHUB_PERSONAL_ACCESS_TOKEN}@github.com\" > /home/node/.git-credentials\n"+
			"git config --global --add safe.directory /workspaces/%s\n"+
			"git config --global user.name \"${GIT_AUTHOR_NAME}\"\n"+
			"git config --global user.email \"${GIT_AUTHOR_EMAIL}\"\n"+
			"git clone https://github.com/%s /workspaces/%s\n"+
			"cd /workspaces/%s\n"+
			// Remove .mcp.json so claude does not try to spawn Node.js MCP servers,
			// which get OOMKilled due to Docker VM swap exhaustion. gh CLI covers all
			// GitHub operations we need (gh issue view, gh pr create, gh issue comment).
			"rm -f .mcp.json\n"+
			"claude -p %q "+
			"--allowedTools 'Read,Edit,Write,Bash' "+
			"--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
		repo, task.Spec.Repo, repo, repo, prompt,
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
				Image:   agentImage,
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
					{Name: "ANTHROPIC_API_KEY", ValueFrom: secretRef(task, "claude-token")},
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
			"6. Push: `git push -u origin claude/issue-%d`\n"+
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

func ensurePod(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	return client.IgnoreAlreadyExists(c.Create(ctx, pod))
}

func getPod(ctx context.Context, c client.Client, ns string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentPodName}, pod)
	return pod, err
}
