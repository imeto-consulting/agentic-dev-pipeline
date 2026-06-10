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
	"slices"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const (
	pollInterval          = 30 * time.Second
	readyLabel            = "ready-for-development"
	defaultMaxConcurrent  = 3
	maxConcurrentTasksEnv = "MAX_CONCURRENT_TASKS"
)

// maxConcurrentTasks is the ceiling on simultaneously-active DevTasks across all
// watched repos. Each active task is a credentialed agent pod, so on a public
// repo this bounds both spend and blast radius when many issues are labeled at
// once. Override with MAX_CONCURRENT_TASKS.
func maxConcurrentTasks() int {
	if v := os.Getenv(maxConcurrentTasksEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxConcurrent
}

// countActiveDevTasks returns the number of DevTasks not in a terminal phase.
func countActiveDevTasks(ctx context.Context, c client.Client) (int, error) {
	list := &devpipelinev1alpha1.DevTaskList{}
	if err := c.List(ctx, list, client.InNamespace(systemNamespace)); err != nil {
		return 0, err
	}
	active := 0
	for i := range list.Items {
		switch list.Items[i].Status.Phase {
		case devpipelinev1alpha1.PhaseCompleted, devpipelinev1alpha1.PhaseFailed:
			// terminal — does not count against the cap
		default:
			active++
		}
	}
	return active, nil
}

// StartGitHubPoller polls GitHub every 30s and creates DevTask CRs for labeled issues.
func StartGitHubPoller(ctx context.Context, c client.Client, repos []string) {
	logger := log.FromContext(ctx).WithName("github-poller")
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, repo := range repos {
					if err := pollRepo(ctx, c, repo); err != nil {
						logger.Error(err, "poll failed", "repo", repo)
					}
				}
			}
		}
	}()
	logger.Info("GitHub poller started", "repos", repos, "interval", pollInterval)
}

func pollRepo(ctx context.Context, c client.Client, repo string) error {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return err
	}

	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format %q (expected owner/name)", repo)
	}
	owner, name := parts[0], parts[1]

	ghClient := newGHClient(ctx, creds.githubToken)
	issues, _, err := ghClient.Issues.ListByRepo(ctx, owner, name, &gh.IssueListByRepoOptions{
		Labels: []string{readyLabel},
		State:  "open",
	})
	if err != nil {
		return fmt.Errorf("list issues: %w", err)
	}

	active, err := countActiveDevTasks(ctx, c)
	if err != nil {
		return fmt.Errorf("count active devtasks: %w", err)
	}
	limit := maxConcurrentTasks()

	for _, issue := range issues {
		if issue.PullRequestLinks != nil {
			continue // GitHub returns PRs in the issues list; skip them
		}
		created, err := ensureDevTask(ctx, c, repo, issue.GetNumber(), active < limit)
		if err != nil {
			log.FromContext(ctx).Error(err, "ensure devtask", "issue", issue.GetNumber())
			continue
		}
		if created {
			active++
		} else if active >= limit {
			log.FromContext(ctx).Info("at concurrency cap, deferring issue",
				"issue", issue.GetNumber(), "active", active, "cap", limit)
		}
	}
	return nil
}

// ensureDevTask creates a DevTask for the issue if one does not already exist.
// Returns (created, err). When allowCreate is false and no task exists yet, it
// is a no-op (the concurrency cap is full) — the issue keeps its label and is
// reconsidered on the next poll.
func ensureDevTask(ctx context.Context, c client.Client, repo string, issueNumber int, allowCreate bool) (bool, error) {
	name := fmt.Sprintf("%s-%d", repoName(repo), issueNumber)

	existing := &devpipelinev1alpha1.DevTask{}
	err := c.Get(ctx, client.ObjectKey{Namespace: systemNamespace, Name: name}, existing)
	if err == nil {
		// Don't restart terminal tasks automatically
		return false, nil
	}
	if client.IgnoreNotFound(err) != nil {
		return false, err
	}
	if !allowCreate {
		return false, nil
	}

	task := &devpipelinev1alpha1.DevTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: systemNamespace,
		},
		Spec: devpipelinev1alpha1.DevTaskSpec{
			IssueNumber: issueNumber,
			Repo:        repo,
		},
	}
	log.FromContext(ctx).Info("Creating DevTask for labeled issue", "repo", repo, "issue", issueNumber)
	if err := c.Create(ctx, task); err != nil {
		return false, err
	}
	return true, nil
}

// findPRForTask returns the PR for a DevTask, by recorded PRNumber if present
// or by searching for the canonical branch name. Returns (nil, nil) if no PR
// exists yet.
func findPRForTask(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) (*gh.PullRequest, error) {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)

	if task.Status.PRNumber != 0 {
		pr, _, err := ghClient.PullRequests.Get(ctx, parts[0], parts[1], task.Status.PRNumber)
		if err != nil {
			return nil, err
		}
		return pr, nil
	}

	// Branch names are either claude/issue-N (legacy) or claude/issue-N-some-slug (current).
	// List open PRs and prefix-match on the issue number so a slug change doesn't break detection.
	prefix := fmt.Sprintf("claude/issue-%d-", task.Spec.IssueNumber)
	legacy := fmt.Sprintf("claude/issue-%d", task.Spec.IssueNumber)
	prs, _, err := ghClient.PullRequests.List(ctx, parts[0], parts[1], &gh.PullRequestListOptions{
		State: "open",
	})
	if err != nil {
		return nil, err
	}
	for _, pr := range prs {
		ref := pr.GetHead().GetRef()
		if strings.HasPrefix(ref, prefix) || ref == legacy {
			return pr, nil
		}
	}
	return nil, nil
}

// isPRMergedOrClosed checks whether the PR for a DevTask has been merged or closed.
func isPRMergedOrClosed(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) (bool, error) {
	pr, err := findPRForTask(ctx, c, task)
	if err != nil || pr == nil {
		return false, err
	}
	return pr.GetMerged() || pr.GetState() == "closed", nil
}

// ensurePRCommentOnIssue posts "PR: <url>" on the issue if no prior comment
// already references a PR URL. Idempotent: safe to call on every reconcile.
func ensurePRCommentOnIssue(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, prURL string) error {
	if prURL == "" {
		return nil
	}
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	comments, _, err := ghClient.Issues.ListComments(ctx, parts[0], parts[1], task.Spec.IssueNumber, nil)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		if strings.Contains(comment.GetBody(), "PR: https://") {
			return nil
		}
	}
	body := "PR: " + prURL
	_, _, err = ghClient.Issues.CreateComment(ctx, parts[0], parts[1], task.Spec.IssueNumber, &gh.IssueComment{Body: &body})
	return err
}

// hasRecentClarificationComment checks if the agent posted a /clarification comment on the issue.
func hasRecentClarificationComment(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) (bool, error) {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return false, err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	comments, _, err := ghClient.Issues.ListComments(ctx, parts[0], parts[1], task.Spec.IssueNumber, nil)
	if err != nil {
		return false, err
	}
	for _, comment := range comments {
		if strings.HasPrefix(comment.GetBody(), "/clarification:") {
			return true, nil
		}
	}
	return false, nil
}

// trustedAuthorAssociations are the GitHub author_association values we accept
// as authorized to steer a resumed agent. On a public repo, ANY user can
// comment on an issue, so "last comment is from a non-bot" is not enough — an
// attacker's comment would otherwise feed straight into a credentialed agent's
// resume prompt. Only repo owners/members/collaborators may answer a
// /clarification and trigger a resume.
var trustedAuthorAssociations = map[string]bool{
	"OWNER":        true,
	"MEMBER":       true,
	"COLLABORATOR": true,
}

// humanRepliedAfterClarification returns true if the last issue comment is from
// an authorized human (a repo owner/member/collaborator, not a bot). The author
// association check is the authorization gate for resuming a blocked task.
func humanRepliedAfterClarification(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) (bool, error) {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return false, err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	comments, _, err := ghClient.Issues.ListComments(ctx, parts[0], parts[1], task.Spec.IssueNumber, nil)
	if err != nil || len(comments) == 0 {
		return false, err
	}
	last := comments[len(comments)-1]
	if last.GetUser().GetType() == "Bot" {
		return false, nil
	}
	botLogins := []string{"github-actions[bot]", "app/github-actions"}
	if slices.Contains(botLogins, last.GetUser().GetLogin()) {
		return false, nil
	}
	// Authorization gate: only trusted associations may resume the agent.
	return trustedAuthorAssociations[last.GetAuthorAssociation()], nil
}

// prHasLabel returns true if the PR associated with the DevTask has the given label.
func prHasLabel(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, label string) (bool, error) {
	if task.Status.PRNumber == 0 {
		return false, nil
	}
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return false, err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	pr, _, err := ghClient.PullRequests.Get(ctx, parts[0], parts[1], task.Status.PRNumber)
	if err != nil {
		return false, err
	}
	for _, l := range pr.Labels {
		if l.GetName() == label {
			return true, nil
		}
	}
	return false, nil
}

// removePRLabel removes a label from the PR associated with the DevTask.
func removePRLabel(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, label string) error {
	if task.Status.PRNumber == 0 {
		return nil
	}
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	_, err = ghClient.Issues.RemoveLabelForIssue(ctx, parts[0], parts[1], task.Status.PRNumber, label)
	return err
}

// listPRFiles returns the changed files for a PR (additions/deletions per file
// and the path list), paging through all results. Used by the diff policy.
func listPRFiles(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, prNumber int) ([]*gh.CommitFile, error) {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)

	var all []*gh.CommitFile
	opts := &gh.ListOptions{PerPage: 100}
	for {
		files, resp, err := ghClient.PullRequests.ListFiles(ctx, parts[0], parts[1], prNumber, opts)
		if err != nil {
			return nil, err
		}
		all = append(all, files...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return all, nil
}

// getIssueBody returns the issue body text for diff-policy approve-risky-paths parsing.
func getIssueBody(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) (string, error) {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)
	issue, _, err := ghClient.Issues.Get(ctx, parts[0], parts[1], task.Spec.IssueNumber)
	if err != nil {
		return "", err
	}
	return issue.GetBody(), nil
}

// rejectPRForDiffPolicy posts a comment listing the violations and closes the PR.
func rejectPRForDiffPolicy(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, prNumber int, violations []DiffViolation) error {
	creds, err := readPipelineCredentials(ctx, c)
	if err != nil {
		return err
	}
	parts := strings.SplitN(task.Spec.Repo, "/", 2)
	ghClient := newGHClient(ctx, creds.githubToken)

	var b strings.Builder
	b.WriteString("Automated diff policy rejected this PR. The agent's changes were not forwarded for review:\n\n")
	for _, v := range violations {
		b.WriteString("- " + v.Detail + "\n")
	}
	b.WriteString("\nThis PR has been closed. Adjust the issue (or its `approve-risky-paths:` token) and re-trigger if appropriate.")
	body := b.String()
	if _, _, err := ghClient.Issues.CreateComment(ctx, parts[0], parts[1], prNumber, &gh.IssueComment{Body: &body}); err != nil {
		return err
	}
	state := "closed"
	_, _, err = ghClient.PullRequests.Edit(ctx, parts[0], parts[1], prNumber, &gh.PullRequest{State: &state})
	return err
}

func newGHClient(ctx context.Context, token string) *gh.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return gh.NewClient(oauth2.NewClient(ctx, ts))
}
