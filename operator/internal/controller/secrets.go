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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

const systemNamespace = "devpipeline-system"

type pipelineCreds struct {
	githubToken    string
	claudeToken    string
	claudeAuthMode string
	gitAuthorName  string
	gitAuthorEmail string
}

func taskSecretName(task *devpipelinev1alpha1.DevTask) string {
	return fmt.Sprintf("devtask-%d-creds", task.Spec.IssueNumber)
}

// ensureTaskSecret copies pipeline credentials into the task namespace as a Secret.
func ensureTaskSecret(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, creds pipelineCreds) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskSecretName(task),
			Namespace: taskNamespace(task),
		},
		StringData: map[string]string{
			"github-token":     creds.githubToken,
			"claude-token":     creds.claudeToken,
			"claude-auth-mode": creds.claudeAuthMode,
			"git-author-name":  creds.gitAuthorName,
			"git-author-email": creds.gitAuthorEmail,
		},
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, secret))
}

// readPipelineCredentials reads credentials from the pipeline-creds Secret in devpipeline-system.
// Falls back to operator env vars if the Secret does not exist.
func readPipelineCredentials(ctx context.Context, c client.Client) (pipelineCreds, error) {
	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{Namespace: systemNamespace, Name: "pipeline-creds"}, secret)
	if err != nil {
		return pipelineCreds{}, fmt.Errorf("read pipeline-creds secret: %w", err)
	}
	return pipelineCreds{
		githubToken:    string(secret.Data["github-token"]),
		claudeToken:    string(secret.Data["claude-token"]),
		claudeAuthMode: string(secret.Data["claude-auth-mode"]),
		gitAuthorName:  string(secret.Data["git-author-name"]),
		gitAuthorEmail: string(secret.Data["git-author-email"]),
	}, nil
}
