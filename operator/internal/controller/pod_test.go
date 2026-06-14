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
	"slices"
	"testing"

	devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

func testTask() *devpipelinev1alpha1.DevTask {
	return &devpipelinev1alpha1.DevTask{
		Spec: devpipelinev1alpha1.DevTaskSpec{IssueNumber: 7, Repo: "owner/repo"},
	}
}

func TestAgentPodSecurityContext(t *testing.T) {
	pod := agentPod(testTask())

	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("agent pod must set automountServiceAccountToken=false")
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("agent pod must run as non-root")
	}

	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 agent container, got %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]
	sc := c.SecurityContext
	if sc == nil {
		t.Fatal("agent container has no securityContext")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("agent container must have readOnlyRootFilesystem=true")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("agent container must set allowPrivilegeEscalation=false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 || string(sc.Capabilities.Drop[0]) != "ALL" {
		t.Error("agent container must drop ALL capabilities")
	}
	if c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Error("agent container must declare CPU and memory limits")
	}

	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Image != busyboxImage {
		t.Errorf("init container must use pinned %s", busyboxImage)
	}
}

func TestAgentPodEgressProxyEnv(t *testing.T) {
	envNames := func() []string {
		env := agentPod(testTask()).Spec.Containers[0].Env
		names := make([]string, 0, len(env))
		for _, e := range env {
			names = append(names, e.Name)
		}
		return names
	}

	// Default mode: no proxy env.
	t.Setenv(egressProxyURLEnv, "")
	if slices.Contains(envNames(), "HTTPS_PROXY") {
		t.Error("default mode must not inject HTTPS_PROXY")
	}

	// Proxy mode: proxy env injected.
	t.Setenv(egressProxyURLEnv, "http://egress-proxy.egress-proxy.svc.cluster.local:3128")
	names := envNames()
	for _, want := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if !slices.Contains(names, want) {
			t.Errorf("proxy mode must inject %s", want)
		}
	}
}
