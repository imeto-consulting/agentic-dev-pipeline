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
		pod := agentPod(task)
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
		return r.reconcileActivePod(ctx, task)

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
		if err := removePRLabel(ctx, r.Client, task, "needs-revision"); err != nil {
			logger.Error(err, "failed to remove needs-revision label")
		}
		// Delete a previous agent-rev pod if it exists (completed from an earlier revision cycle).
		_ = deleteRevisionPod(ctx, r.Client, task.Status.Namespace)
		return r.spawnAgentSandbox(ctx, task, agentPodRevision(task), "addressing review feedback")

	case devpipelinev1alpha1.PhaseBlockedOnClarification:
		humanReplied, err := humanRepliedAfterClarification(ctx, r.Client, task)
		if err != nil || !humanReplied {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		return r.spawnAgentSandbox(ctx, task, agentPodResume(task), "resuming after clarification")

	case devpipelinev1alpha1.PhaseCompleted:
		return ctrl.Result{}, nil

	case devpipelinev1alpha1.PhaseFailed:
		// TTL the task namespace so a failed task's per-task Secret (GitHub +
		// Claude tokens) doesn't linger indefinitely in a dead namespace.
		if task.Status.Namespace == "" || task.Status.FailedAt == nil {
			return ctrl.Result{}, nil
		}
		elapsed := time.Since(task.Status.FailedAt.Time)
		if elapsed < failedNamespaceTTL {
			return ctrl.Result{RequeueAfter: failedNamespaceTTL - elapsed}, nil
		}
		if err := deleteNamespace(ctx, r.Client, task.Status.Namespace); err != nil {
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		logger.Info("deleted failed task namespace after TTL", "namespace", task.Status.Namespace)
		task.Status.Namespace = ""
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// failedNamespaceTTL is how long a failed task's namespace (and its per-task
// Secret) survives before the operator tears it down. Override with
// FAILED_NAMESPACE_TTL (Go duration string, e.g. "30m").
var failedNamespaceTTL = loadFailedNamespaceTTL()

func loadFailedNamespaceTTL() time.Duration {
	if v := os.Getenv("FAILED_NAMESPACE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return time.Hour
}

// markFailed sets the terminal Failed phase and stamps FailedAt for the
// namespace TTL. It does not delete the namespace — the Failed-case reconcile
// does that once the TTL elapses, leaving a window to inspect logs.
func markFailed(task *devpipelinev1alpha1.DevTask, message string) {
	now := metav1.Now()
	task.Status.Phase = devpipelinev1alpha1.PhaseFailed
	task.Status.Message = message
	task.Status.FailedAt = &now
}

// spawnAgentSandbox (re)creates the task namespace, NetworkPolicy, and per-task
// Secret, spawns the given agent pod, and advances the DevTask to Building.
// Shared by the revision and clarification-resume flows.
func (r *DevTaskReconciler) spawnAgentSandbox(ctx context.Context, task *devpipelinev1alpha1.DevTask, pod *corev1.Pod, message string) (ctrl.Result, error) {
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
	if err := ensurePod(ctx, r.Client, pod); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure pod: %w", err)
	}
	now := metav1.Now()
	task.Status.Phase = devpipelinev1alpha1.PhaseBuilding
	task.Status.Namespace = taskNamespace(task)
	task.Status.StartedAt = &now
	task.Status.Message = message
	return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)
}

// reconcileActivePod drives a Building/Running DevTask off the agent pod's
// Kubernetes phase: Running → mark Running; Succeeded → PR + diff policy;
// Failed → clarification handoff or terminal failure.
func (r *DevTaskReconciler) reconcileActivePod(ctx context.Context, task *devpipelinev1alpha1.DevTask) (ctrl.Result, error) {
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
		return r.handlePodSucceeded(ctx, task)
	case corev1.PodFailed:
		clarified, cerr := hasRecentClarificationComment(ctx, r.Client, task)
		if cerr == nil && clarified {
			_ = deleteNamespace(ctx, r.Client, task.Status.Namespace)
			task.Status.Phase = devpipelinev1alpha1.PhaseBlockedOnClarification
			task.Status.Message = "agent requested clarification"
			return ctrl.Result{RequeueAfter: time.Minute}, r.Status().Update(ctx, task)
		}
		markFailed(task, "agent pod failed")
		return ctrl.Result{RequeueAfter: failedNamespaceTTL}, r.Status().Update(ctx, task)
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handlePodSucceeded verifies a real PR exists, runs the diff policy, and either
// rejects the PR (terminal failure) or advances to AwaitingReview.
func (r *DevTaskReconciler) handlePodSucceeded(ctx context.Context, task *devpipelinev1alpha1.DevTask) (ctrl.Result, error) {
	// Pod exited 0 doesn't guarantee a PR exists — claude -p frequently returns
	// 0 even when the final gh pr create failed. Verify the PR is real first.
	pr, err := findPRForTask(ctx, r.Client, task)
	if err != nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}
	if pr == nil {
		markFailed(task, "agent pod exited 0 but no PR was opened on the canonical branch")
		return ctrl.Result{RequeueAfter: failedNamespaceTTL}, r.Status().Update(ctx, task)
	}
	// Diff policy: reject PRs touching restricted/risky paths or exceeding size
	// caps before forwarding them for human review.
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
		markFailed(task, "diff policy: "+violations[0].Reason)
		task.Status.Namespace = ""
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
}

// SetupWithManager sets up the controller with the Manager.
func (r *DevTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&devpipelinev1alpha1.DevTask{}).
		Named("devtask").
		Complete(r)
}
