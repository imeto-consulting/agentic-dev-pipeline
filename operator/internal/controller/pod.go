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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const (
	envbuilderImage = "ghcr.io/coder/envbuilder:latest"
	agentPodName    = "agent"
	registryBase    = "slaktforskning-registry:5050"
)

func int64Ptr(i int64) *int64 { return &i }

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
			"1. Read the issue via the GitHub MCP (mcp__github). Follow the plan in the issue body.\n"+
			"2. Work on branch claude/issue-%d (create or check out).\n"+
			"3. Run tests. Iterate until they pass.\n"+
			"4. Commit with --signoff: `git commit -s -m \"...\"`. Every commit needs Signed-off-by.\n"+
			"5. Push. Open a PR against main. Comment on the issue with the PR URL.\n\n"+
			"If blocked: commit WIP with -s, push, open a draft PR, comment '/clarification:' on the issue, exit 2.\n"+
			"Do not touch .devcontainer/, .mcp.json, or .github/workflows/ unless the issue specifically asks.",
		task.Spec.IssueNumber, task.Spec.Repo, task.Spec.IssueNumber,
	)
}

func agentPod(task *devpipelinev1alpha1.DevTask, githubToken, anthropicKey string) *corev1.Pod {
	ns := taskNamespace(task)
	repo := repoName(task.Spec.Repo)
	cacheRepo := registryBase + "/" + repo + "-devcontainer"
	prompt := buildAgentPrompt(task)

	runScript := fmt.Sprintf(
		"#!/bin/bash\nset -e\ncd /workspaces/%s\nclaude -p %q "+
			"--allowedTools 'Read,Edit,Write,Bash,mcp__github' "+
			"--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
		repo, prompt,
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
			InitContainers: []corev1.Container{{
				Name:    "write-script",
				Image:   "busybox",
				Command: []string{"sh", "-c", "printf '%s' \"$SCRIPT\" > /tmp/run-agent.sh && chmod +x /tmp/run-agent.sh"},
				Env:     []corev1.EnvVar{{Name: "SCRIPT", Value: runScript}},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "tmp", MountPath: "/tmp"},
				},
			}},
			Containers: []corev1.Container{{
				Name:  "agent",
				Image: envbuilderImage,
				Env: []corev1.EnvVar{
					{Name: "ENVBUILDER_REPO_URL", Value: "https://github.com/" + task.Spec.Repo},
					{Name: "ENVBUILDER_CACHE_REPO", Value: cacheRepo},
					{Name: "ENVBUILDER_POST_START_SCRIPT_PATH", Value: "/tmp/run-agent.sh"},
					{Name: "GITHUB_PERSONAL_ACCESS_TOKEN", Value: githubToken},
					{Name: "ANTHROPIC_API_KEY", Value: anthropicKey},
					// Git identity required for DCO: git commit -s generates Signed-off-by from these.
					// Moved to per-task Secrets in Phase 3.
					{Name: "GIT_AUTHOR_NAME", Value: "Jonas Ahnstedt"},
					{Name: "GIT_AUTHOR_EMAIL", Value: "jonas.ahnstedt@imeto.se"},
					{Name: "GIT_COMMITTER_NAME", Value: "Jonas Ahnstedt"},
					{Name: "GIT_COMMITTER_EMAIL", Value: "jonas.ahnstedt@imeto.se"},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "workdir", MountPath: "/workspaces"},
					{Name: "tmp", MountPath: "/tmp"},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "workdir", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
}

func ensurePod(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	return client.IgnoreAlreadyExists(c.Create(ctx, pod))
}

func getPod(ctx context.Context, c client.Client, ns string) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentPodName}, pod)
	return pod, err
}
