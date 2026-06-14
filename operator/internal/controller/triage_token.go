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
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Triage CronJob token rotation. The triage job reads github-token from the
// pipeline-creds Secret in its own namespace. In PAT mode that's the long-lived
// PAT. In GitHub App mode the operator (which holds the App private key) mints a
// ~1h installation token and writes it into that same key on a timer, so the
// triage job — like the agent pods — no longer relies on a long-lived secret.
//
// The triage job can't mint its own token: minting needs the App private key
// and JWT signing, which we don't want to ship into the triage sandbox. The
// operator is the right place for the key, so it pushes fresh tokens out.

const (
	triageNamespaceEnv      = "TRIAGE_NAMESPACE"
	defaultTriageNamespace  = "agentic-dev-pipeline-triage"
	triageTokenRefreshEvery = 45 * time.Minute // installation tokens TTL at ~1h
)

func triageNamespace() string {
	if v := os.Getenv(triageNamespaceEnv); v != "" {
		return v
	}
	return defaultTriageNamespace
}

// StartTriageTokenRefresher keeps the triage namespace's github-token fresh with
// a minted GitHub App installation token. No-op in PAT mode (githubAppToken
// returns ""), so it is always safe to start.
func StartTriageTokenRefresher(ctx context.Context, c client.Client) {
	logger := log.FromContext(ctx).WithName("triage-token-refresher")

	refresh := func() {
		token, err := githubAppToken(ctx, c)
		if err != nil {
			logger.Error(err, "mint installation token for triage")
			return
		}
		if token == "" {
			return // PAT mode — nothing to rotate
		}
		if err := upsertTriageGithubToken(ctx, c, token); err != nil {
			logger.Error(err, "write triage github token")
			return
		}
		logger.Info("refreshed triage github token from GitHub App installation")
	}

	go func() {
		refresh() // once at startup
		ticker := time.NewTicker(triageTokenRefreshEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
	logger.Info("triage token refresher started", "namespace", triageNamespace(), "interval", triageTokenRefreshEvery)
}

// upsertTriageGithubToken updates only the github-token key of the triage
// pipeline-creds Secret, preserving the Claude token + auth mode. If the Secret
// doesn't exist yet (make secrets not run), it logs and skips rather than
// creating a partial Secret that would break the triage job.
func upsertTriageGithubToken(ctx context.Context, c client.Client, token string) error {
	ns := triageNamespace()
	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "pipeline-creds"}, secret)
	if apierrors.IsNotFound(err) {
		log.FromContext(ctx).Info("triage pipeline-creds Secret not found; run `make secrets`", "namespace", ns)
		return nil
	}
	if err != nil {
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data["github-token"] = []byte(token)
	return c.Update(ctx, secret)
}
