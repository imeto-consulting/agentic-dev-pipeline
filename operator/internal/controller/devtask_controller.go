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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

// DevTaskReconciler reconciles a DevTask object
type DevTaskReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=devpipeline.devpipeline.local,resources=devtasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=devpipeline.devpipeline.local,resources=devtasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=devpipeline.devpipeline.local,resources=devtasks/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;delete

func (r *DevTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	task := &devpipelinev1alpha1.DevTask{}
	if err := r.Get(ctx, req.NamespacedName, task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch task.Status.Phase {
	case "":
		logger.Info("New DevTask", "issue", task.Spec.IssueNumber)
		if err := ensureNamespace(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
		}
		if err := ensureNetworkPolicy(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure network policy: %w", err)
		}
		creds, err := readPipelineCredentials(ctx, r.Client)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("read credentials: %w", err)
		}
		if err := ensureTaskSecret(ctx, r.Client, task, creds); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure task secret: %w", err)
		}
		pod := agentPod(task, creds.githubToken, creds.claudeToken)
		if err := ensurePod(ctx, r.Client, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure pod: %w", err)
		}
		now := metav1.Now()
		task.Status.Phase = devpipelinev1alpha1.PhaseBuilding
		task.Status.Namespace = taskNamespace(task)
		task.Status.StartedAt = &now
		task.Status.Message = "envbuilder building devcontainer"
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)

	case devpipelinev1alpha1.PhaseBuilding, devpipelinev1alpha1.PhaseRunning:
		pod, err := getPod(ctx, r.Client, task.Status.Namespace)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, client.IgnoreNotFound(err)
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			if task.Status.Phase != devpipelinev1alpha1.PhaseRunning {
				task.Status.Phase = devpipelinev1alpha1.PhaseRunning
				task.Status.Message = "agent running"
				return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)
			}
		case corev1.PodSucceeded:
			// Pod exited 0 doesn't guarantee a PR exists — claude -p frequently
			// returns 0 even when the final gh pr create failed. Verify the PR is
			// real before transitioning to AwaitingReview.
			pr, err := findPRForTask(ctx, r.Client, task)
			if err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
			if pr == nil {
				task.Status.Phase = devpipelinev1alpha1.PhaseFailed
				task.Status.Message = "agent pod exited 0 but no PR was opened on the canonical branch"
				return ctrl.Result{}, r.Status().Update(ctx, task)
			}
				// Diff policy: reject PRs touching restricted/risky paths or exceeding
				// size caps before forwarding them for human review.
				files, ferr := listPRFiles(ctx, r.Client, task, pr.GetNumber())
				if ferr != nil {
					return ctrl.Result{RequeueAfter: 30 * time.Second}, ferr
				}
				issueBody, berr := getIssueBody(ctx, r.Client, task)
				if berr != nil {
					return ctrl.Result{RequeueAfter: 30 * time.Second}, berr
				}
				if violations := evaluateDiff(files, issueBody, loadDiffPolicy()); len(violations) > 0 {
					if rerr := rejectPRForDiffPolicy(ctx, r.Client, task, pr.GetNumber(), violations); rerr != nil {
						return ctrl.Result{RequeueAfter: 30 * time.Second}, rerr
					}
					_ = deleteNamespace(ctx, r.Client, task.Status.Namespace)
					task.Status.Phase = devpipelinev1alpha1.PhaseFailed
					task.Status.Message = "diff policy: " + violations[0].Reason
					return ctrl.Result{}, r.Status().Update(ctx, task)
				}
			// Post "PR: <url>" on the issue ourselves rather than asking the agent to.
			// The agent's bash wrapper mangles multi-arg gh commands, leaving artifacts
			// like an empty "PR: " comment followed by a separate URL-only comment.
			if cerr := ensurePRCommentOnIssue(ctx, r.Client, task, pr.GetHTMLURL()); cerr != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, cerr
			}
			task.Status.PRNumber = pr.GetNumber()
			task.Status.Phase = devpipelinev1alpha1.PhaseAwaitingReview
			task.Status.Message = "agent completed"
			return ctrl.Result{RequeueAfter: 2 * time.Minute}, r.Status().Update(ctx, task)
		case corev1.PodFailed:
			clarified, cerr := hasRecentClarificationComment(ctx, r.Client, task)
			if cerr == nil && clarified {
				_ = deleteNamespace(ctx, r.Client, task.Status.Namespace)
				task.Status.Phase = devpipelinev1alpha1.PhaseBlockedOnClarification
				task.Status.Message = "agent requested clarification"
				return ctrl.Result{RequeueAfter: time.Minute}, r.Status().Update(ctx, task)
			}
			task.Status.Phase = devpipelinev1alpha1.PhaseFailed
			task.Status.Message = "agent pod failed"
			return ctrl.Result{}, r.Status().Update(ctx, task)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

	case devpipelinev1alpha1.PhaseAwaitingReview:
		merged, err := isPRMergedOrClosed(ctx, r.Client, task)
		if err != nil {
			return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
		}
		if merged {
			_ = deleteNamespace(ctx, r.Client, task.Status.Namespace)
			task.Status.Phase = devpipelinev1alpha1.PhaseCompleted
			task.Status.Message = "PR merged or closed, namespace deleted"
			return ctrl.Result{}, r.Status().Update(ctx, task)
		}
		needsRevision, err := prHasLabel(ctx, r.Client, task, "needs-revision")
		if err != nil {
			return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
		}
		if needsRevision {
			task.Status.Phase = devpipelinev1alpha1.PhaseAwaitingRevision
			task.Status.Message = "reviewer requested changes"
			return ctrl.Result{}, r.Status().Update(ctx, task)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil

	case devpipelinev1alpha1.PhaseAwaitingRevision:
		creds, err := readPipelineCredentials(ctx, r.Client)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("read credentials: %w", err)
		}
		if err := removePRLabel(ctx, r.Client, task, "needs-revision"); err != nil {
			logger.Error(err, "failed to remove needs-revision label")
		}
		if err := ensureNamespace(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
		}
		if err := ensureNetworkPolicy(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure network policy: %w", err)
		}
		if err := ensureTaskSecret(ctx, r.Client, task, creds); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure task secret: %w", err)
		}
		// Delete a previous agent-rev pod if it exists (completed from an earlier revision cycle).
		_ = deleteRevisionPod(ctx, r.Client, task.Status.Namespace)
		pod := agentPodRevision(task)
		if err := ensurePod(ctx, r.Client, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure pod: %w", err)
		}
		now := metav1.Now()
		task.Status.Phase = devpipelinev1alpha1.PhaseBuilding
		task.Status.StartedAt = &now
		task.Status.Message = "addressing review feedback"
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)

	case devpipelinev1alpha1.PhaseBlockedOnClarification:
		humanReplied, err := humanRepliedAfterClarification(ctx, r.Client, task)
		if err != nil || !humanReplied {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		creds, err := readPipelineCredentials(ctx, r.Client)
		if err != nil {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("read credentials: %w", err)
		}
		if err := ensureNamespace(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
		}
		if err := ensureNetworkPolicy(ctx, r.Client, task); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure network policy: %w", err)
		}
		if err := ensureTaskSecret(ctx, r.Client, task, creds); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure task secret: %w", err)
		}
		pod := agentPodResume(task)
		if err := ensurePod(ctx, r.Client, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure pod: %w", err)
		}
		now := metav1.Now()
		task.Status.Phase = devpipelinev1alpha1.PhaseBuilding
		task.Status.Namespace = taskNamespace(task)
		task.Status.StartedAt = &now
		task.Status.Message = "resuming after clarification"
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)

	case devpipelinev1alpha1.PhaseCompleted, devpipelinev1alpha1.PhaseFailed:
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DevTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&devpipelinev1alpha1.DevTask{}).
		Named("devtask").
		Complete(r)
}
