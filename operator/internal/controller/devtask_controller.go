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
		pod := agentPod(task, os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN"), os.Getenv("ANTHROPIC_API_KEY"))
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
			task.Status.Phase = devpipelinev1alpha1.PhaseAwaitingReview
			task.Status.Message = "agent completed"
			return ctrl.Result{RequeueAfter: 2 * time.Minute}, r.Status().Update(ctx, task)
		case corev1.PodFailed:
			task.Status.Phase = devpipelinev1alpha1.PhaseFailed
			task.Status.Message = "agent pod failed"
			return ctrl.Result{}, r.Status().Update(ctx, task)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

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
