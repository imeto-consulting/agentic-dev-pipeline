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
	"time"

	gh "github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const (
	pollInterval = 30 * time.Second
	readyLabel   = "ready-for-development"
)

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

	for _, issue := range issues {
		if issue.PullRequestLinks != nil {
			continue // GitHub returns PRs in the issues list; skip them
		}
		if err := ensureDevTask(ctx, c, repo, int(issue.GetNumber())); err != nil {
			log.FromContext(ctx).Error(err, "ensure devtask", "issue", issue.GetNumber())
		}
	}
	return nil
}

func ensureDevTask(ctx context.Context, c client.Client, repo string, issueNumber int) error {
	name := fmt.Sprintf("%s-%d", repoName(repo), issueNumber)

	existing := &devpipelinev1alpha1.DevTask{}
	err := c.Get(ctx, client.ObjectKey{Namespace: systemNamespace, Name: name}, existing)
	if err == nil {
		// Don't restart terminal tasks automatically
		return nil
	}
	if client.IgnoreNotFound(err) != nil {
		return err
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
	return c.Create(ctx, task)
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

	branch := fmt.Sprintf("claude/issue-%d", task.Spec.IssueNumber)
	prs, _, err := ghClient.PullRequests.List(ctx, parts[0], parts[1], &gh.PullRequestListOptions{
		State: "all",
		Head:  parts[0] + ":" + branch,
	})
	if err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0], nil
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

// humanRepliedAfterClarification returns true if the last issue comment is from a human (not a bot).
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
	botLogins := []string{"github-actions[bot]", "app/github-actions"}
	for _, bot := range botLogins {
		if last.GetUser().GetLogin() == bot {
			return false, nil
		}
	}
	return true, nil
}

func newGHClient(ctx context.Context, token string) *gh.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return gh.NewClient(oauth2.NewClient(ctx, ts))
}
