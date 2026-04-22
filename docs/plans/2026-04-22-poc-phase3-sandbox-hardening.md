# POC Phase 3: Sandbox Hardening and Automated Trigger

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Harden the sandbox so the agent can only reach pre-approved endpoints, move credentials into Kubernetes Secrets, and close the polling loop so no manual `DevTask` creation is needed.

**Architecture:** Operator polls GitHub every 30s for `ready-for-development` issues and auto-creates `DevTask` CRs. Per-task Secrets hold credentials (not env vars). NetworkPolicy enforced by Calico. Full state machine including `BlockedOnClarification`.

**Tech Stack:** kubebuilder v3, Calico NetworkPolicy, Kubernetes Secrets, `google/go-github` library

**Prerequisite:** Phase 2 complete. `kubectl apply -f devtask-sample.yaml` reliably triggers a PR.

---

### Task 1: Write and verify the NetworkPolicy

**Files:**
- Create: `operator/internal/controller/networkpolicy.go`

- [ ] **Step 1: Create `networkpolicy.go`**

```go
package controller

import (
    "context"

    networkingv1 "k8s.io/api/networking/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/util/intstr"
    "sigs.k8s.io/controller-runtime/pkg/client"

    devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

// ensureNetworkPolicy creates the deny-all + allowlist egress policy for the task namespace.
// Allows: kube-dns (UDP 53), api.github.com (:443), api.anthropic.com (:443),
//         registry.npmjs.org (:443), ghcr.io (:443) for devcontainer build.
func ensureNetworkPolicy(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) error {
    tcp := corev1ProtocolTCP()
    udp := corev1ProtocolUDP()
    port53 := intstr.FromInt(53)
    port443 := intstr.FromInt(443)

    policy := &networkingv1.NetworkPolicy{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "sandbox-egress",
            Namespace: taskNamespace(task),
        },
        Spec: networkingv1.NetworkPolicySpec{
            PodSelector: metav1.LabelSelector{}, // applies to all pods in namespace
            PolicyTypes: []networkingv1.PolicyType{
                networkingv1.PolicyTypeIngress,
                networkingv1.PolicyTypeEgress,
            },
            // Deny all ingress
            Ingress: []networkingv1.NetworkPolicyIngressRule{},
            Egress: []networkingv1.NetworkPolicyEgressRule{
                // kube-dns
                {
                    Ports: []networkingv1.NetworkPolicyPort{
                        {Protocol: &udp, Port: &port53},
                    },
                },
                // External HTTPS (GitHub, Anthropic, package registries, ghcr.io)
                {
                    Ports: []networkingv1.NetworkPolicyPort{
                        {Protocol: &tcp, Port: &port443},
                    },
                },
            },
        },
    }
    return client.IgnoreAlreadyExists(c.Create(ctx, policy))
}

func corev1ProtocolTCP() corev1.Protocol { return corev1.ProtocolTCP }
func corev1ProtocolUDP() corev1.Protocol { return corev1.ProtocolUDP }
```

Add import `corev1 "k8s.io/api/core/v1"` to the file.

- [ ] **Step 2: Call `ensureNetworkPolicy` from the Reconcile function**

In `devtask_controller.go`, in the `case ""` branch (new task), add after `ensureNamespace`:

```go
if err := ensureNetworkPolicy(ctx, r.Client, task); err != nil {
    return ctrl.Result{}, fmt.Errorf("ensure network policy: %w", err)
}
```

- [ ] **Step 3: Build**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

- [ ] **Step 4: Verify NetworkPolicy blocks unexpected egress**

Create a DevTask, wait for the namespace to exist, then test from inside the pod:

```bash
NS=devtask-<ISSUE-NUMBER>
kubectl wait --for=condition=Ready pod/agent -n "${NS}" --timeout=60s

# Should succeed: api.github.com
kubectl exec -n "${NS}" agent -- curl -s --max-time 5 https://api.github.com/zen && echo "PASS: github reachable"

# Should fail: example.com
kubectl exec -n "${NS}" agent -- curl -s --max-time 5 https://example.com && echo "FAIL: example.com reachable" || echo "PASS: example.com blocked"
```

Expected: github reachable, example.com blocked.

- [ ] **Step 5: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/networkpolicy.go operator/internal/controller/devtask_controller.go
git commit -m "feat: NetworkPolicy deny-all + HTTPS allowlist for task sandbox"
```

---

### Task 2: Move credentials to per-task Secrets

**Files:**
- Create: `operator/internal/controller/secrets.go`
- Modify: `operator/internal/controller/pod.go`
- Modify: `operator/internal/controller/devtask_controller.go`

The operator currently reads `GITHUB_TOKEN` and `ANTHROPIC_API_KEY` from its own env and passes them as plain env vars to the pod. Move them to Kubernetes Secrets so they are namespace-scoped and can be individually rotated/revoked.

- [ ] **Step 1: Create `secrets.go`**

```go
package controller

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"

    devpipelinev1alpha1 "github.com/jonaseck2/agentic-dev-pipeline/operator/api/v1alpha1"
)

func taskSecretName(task *devpipelinev1alpha1.DevTask) string {
    return fmt.Sprintf("devtask-%d-creds", task.Spec.IssueNumber)
}

// ensureTaskSecret copies the cluster-level pipeline credentials into the task namespace.
// githubToken and anthropicKey come from a Secret in the system namespace (not env vars).
func ensureTaskSecret(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask, githubToken, anthropicKey string) error {
    secret := &corev1.Secret{
        ObjectMeta: metav1.ObjectMeta{
            Name:      taskSecretName(task),
            Namespace: taskNamespace(task),
        },
        StringData: map[string]string{
            "github-token":    githubToken,
            "anthropic-api-key": anthropicKey,
        },
    }
    return client.IgnoreAlreadyExists(c.Create(ctx, secret))
}

// readPipelineCredentials reads the pipeline's own credentials from a Secret in devpipeline-system.
func readPipelineCredentials(ctx context.Context, c client.Client) (githubToken, anthropicKey string, err error) {
    secret := &corev1.Secret{}
    if err = c.Get(ctx, client.ObjectKey{
        Namespace: "devpipeline-system",
        Name:      "pipeline-creds",
    }, secret); err != nil {
        return "", "", fmt.Errorf("read pipeline-creds secret: %w", err)
    }
    return string(secret.Data["github-token"]), string(secret.Data["anthropic-api-key"]), nil
}
```

- [ ] **Step 2: Update `pod.go` to use SecretKeyRef**

Change the `Env` section in `agentPod` from plain values to secret references:

```go
Env: []corev1.EnvVar{
    {Name: "ENVBUILDER_REPO_URL", Value: "https://github.com/" + task.Spec.Repo},
    {Name: "ENVBUILDER_CACHE_REPO", Value: cacheRepo},
    {Name: "ENVBUILDER_POST_START_SCRIPT_PATH", Value: "/tmp/run-agent.sh"},
    {
        Name: "GITHUB_TOKEN",
        ValueFrom: &corev1.EnvVarSource{
            SecretKeyRef: &corev1.SecretKeySelector{
                LocalObjectReference: corev1.LocalObjectReference{Name: taskSecretName(task)},
                Key: "github-token",
            },
        },
    },
    {
        Name: "ANTHROPIC_API_KEY",
        ValueFrom: &corev1.EnvVarSource{
            SecretKeyRef: &corev1.SecretKeySelector{
                LocalObjectReference: corev1.LocalObjectReference{Name: taskSecretName(task)},
                Key: "anthropic-api-key",
            },
        },
    },
},
```

Remove the `githubToken, anthropicKey string` parameters from `agentPod` since they're no longer needed there.

- [ ] **Step 3: Update Reconcile to use readPipelineCredentials and ensureTaskSecret**

In `devtask_controller.go`, in the `case ""` branch, replace the direct `os.Getenv` calls:

```go
// Read credentials from the system-namespace Secret
githubToken, anthropicKey, err := readPipelineCredentials(ctx, r.Client)
if err != nil {
    return ctrl.Result{}, err
}
if err := ensureTaskSecret(ctx, r.Client, task, githubToken, anthropicKey); err != nil {
    return ctrl.Result{}, fmt.Errorf("ensure task secret: %w", err)
}
pod := agentPod(task)  // no longer takes token params
```

- [ ] **Step 4: Create the pipeline-creds Secret (one-time setup)**

```bash
kubectl create secret generic pipeline-creds \
  --namespace devpipeline-system \
  --from-literal=github-token="${GITHUB_TOKEN}" \
  --from-literal=anthropic-api-key="${ANTHROPIC_API_KEY}"
```

Document this in `CLAUDE.md` under "Local development setup".

- [ ] **Step 5: Build and test**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

Run the operator and apply a test DevTask. Verify the pod starts and completes successfully.

- [ ] **Step 6: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/secrets.go \
        operator/internal/controller/pod.go \
        operator/internal/controller/devtask_controller.go
git commit -m "feat: move credentials to per-task Secrets in task namespace"
```

---

### Task 3: Apply pod security restrictions

**Files:**
- Modify: `operator/internal/controller/pod.go`

- [ ] **Step 1: Add security context to the pod spec**

In `agentPod`, add to `PodSpec`:

```go
SecurityContext: &corev1.PodSecurityContext{
    RunAsNonRoot: boolPtr(true),
    SeccompProfile: &corev1.SeccompProfile{
        Type: corev1.SeccompProfileTypeRuntimeDefault,
    },
},
```

And add to each container's `SecurityContext`:

```go
SecurityContext: &corev1.SecurityContext{
    AllowPrivilegeEscalation: boolPtr(false),
    ReadOnlyRootFilesystem:   boolPtr(true),
    Capabilities: &corev1.Capabilities{
        Drop: []corev1.Capability{"ALL"},
    },
},
```

Add helper:

```go
func boolPtr(b bool) *bool { return &b }
```

Note: envbuilder needs to write to `/workspaces` and `/tmp` — these are already emptyDir mounts, so `ReadOnlyRootFilesystem: true` is fine as long as writable mounts cover what the agent needs. Also add `/home/node/.claude` as an emptyDir for Claude Code's state:

```go
{Name: "claude-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
```

And a mount:

```go
{Name: "claude-state", MountPath: "/home/node/.claude"},
```

- [ ] **Step 2: Build and verify**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

Apply a test DevTask and confirm the pod starts (pod security admission should not reject it).

```bash
kubectl get pod -n devtask-<ISSUE> agent -o jsonpath='{.spec.containers[0].securityContext}' | python3 -m json.tool
```

Expected: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`

- [ ] **Step 3: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/pod.go
git commit -m "feat: pod security context — non-root, read-only rootFS, drop caps"
```

---

### Task 4: GitHub polling — auto-create DevTask CRs

**Files:**
- Modify: `operator/internal/controller/devtask_controller.go`
- Create: `operator/internal/controller/github_poller.go`

Add a polling goroutine that lists `ready-for-development` issues and creates `DevTask` CRs if none exist.

- [ ] **Step 1: Add `google/go-github` dependency**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go get github.com/google/go-github/v60/github
go get golang.org/x/oauth2
go mod tidy
```

- [ ] **Step 2: Create `github_poller.go`**

```go
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
    pollInterval    = 30 * time.Second
    targetRepo      = "jonaseck2/slaktforskning"
    readyLabel      = "ready-for-development"
    systemNamespace = "devpipeline-system"
)

// StartGitHubPoller runs a goroutine that polls GitHub every 30s and creates DevTask CRs.
// It reads the GitHub token from the pipeline-creds Secret each poll cycle.
func StartGitHubPoller(ctx context.Context, c client.Client) {
    logger := log.FromContext(ctx).WithName("github-poller")
    go func() {
        ticker := time.NewTicker(pollInterval)
        defer ticker.Stop()
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                if err := pollGitHub(ctx, c, logger); err != nil {
                    logger.Error(err, "poll failed")
                }
            }
        }
    }()
}

func pollGitHub(ctx context.Context, c client.Client, logger interface{ Info(string, ...interface{}) }) error {
    githubToken, _, err := readPipelineCredentials(ctx, c)
    if err != nil {
        return err
    }

    ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken})
    ghClient := gh.NewClient(oauth2.NewClient(ctx, ts))

    parts := strings.SplitN(targetRepo, "/", 2)
    if len(parts) != 2 {
        return fmt.Errorf("invalid targetRepo: %s", targetRepo)
    }
    owner, repo := parts[0], parts[1]

    issues, _, err := ghClient.Issues.ListByRepo(ctx, owner, repo, &gh.IssueListByRepoOptions{
        Labels: []string{readyLabel},
        State:  "open",
    })
    if err != nil {
        return fmt.Errorf("list issues: %w", err)
    }

    for _, issue := range issues {
        if issue.PullRequestLinks != nil {
            continue // skip PRs (GitHub API returns PRs in issue list)
        }
        if err := ensureDevTask(ctx, c, owner+"/"+repo, int(*issue.Number)); err != nil {
            logger.Info("ensure devtask failed", "issue", *issue.Number, "err", err.Error())
        }
    }
    return nil
}

func ensureDevTask(ctx context.Context, c client.Client, repo string, issueNumber int) error {
    name := fmt.Sprintf("%s-%d", repoName(repo), issueNumber)

    // Check if a non-terminal DevTask already exists
    existing := &devpipelinev1alpha1.DevTask{}
    err := c.Get(ctx, client.ObjectKey{Namespace: systemNamespace, Name: name}, existing)
    if err == nil {
        // Already exists; only recreate if in a terminal state
        if existing.Status.Phase == devpipelinev1alpha1.PhaseCompleted ||
            existing.Status.Phase == devpipelinev1alpha1.PhaseFailed {
            return nil // do not restart terminal tasks automatically
        }
        return nil
    }
    if client.IgnoreNotFound(err) != nil {
        return err
    }

    // Create new DevTask
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
    return c.Create(ctx, task)
}
```

- [ ] **Step 3: Start the poller in main.go**

In `operator/cmd/main.go`, after `mgr.GetClient()` is available, add:

```go
controller.StartGitHubPoller(ctx, mgr.GetClient())
```

Import `controller` package and pass `ctx` from the manager context.

- [ ] **Step 4: Build**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

- [ ] **Step 5: Test: label an issue and watch for auto-created DevTask**

```bash
# Label an issue (use one of the Phase 1 test issues)
gh issue edit <NUMBER> --repo jonaseck2/slaktforskning --add-label "ready-for-development"

# Start the operator
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
make run
```

Wait up to 30 seconds, then:

```bash
kubectl get devtask -n devpipeline-system
```

Expected: A new `DevTask` named `slaktforskning-<NUMBER>` appears automatically.

- [ ] **Step 6: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/github_poller.go operator/cmd/main.go
go.mod go.sum
git commit -m "feat: GitHub poller auto-creates DevTask CRs on ready-for-development label"
```

---

### Task 5: Implement the full state machine including BlockedOnClarification

**Files:**
- Modify: `operator/internal/controller/devtask_controller.go`
- Modify: `operator/internal/controller/github_poller.go`

- [ ] **Step 1: Add AwaitingReview → Completed transition**

In the `PhaseAwaitingReview` case of Reconcile:

```go
case devpipelinev1alpha1.PhaseAwaitingReview:
    githubToken, _, err := readPipelineCredentials(ctx, r.Client)
    if err != nil {
        return ctrl.Result{RequeueAfter: 2 * time.Minute}, err
    }
    merged, err := isPRMergedOrClosed(ctx, task, githubToken)
    if err != nil {
        return ctrl.Result{RequeueAfter: 2 * time.Minute}, err
    }
    if merged {
        if err := deleteNamespace(ctx, r.Client, task.Status.Namespace); err != nil {
            return ctrl.Result{}, err
        }
        task.Status.Phase = devpipelinev1alpha1.PhaseCompleted
        task.Status.Message = "PR merged or closed, namespace deleted"
        return ctrl.Result{}, r.Status().Update(ctx, task)
    }
    return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
```

Add `isPRMergedOrClosed` to `github_poller.go`:

```go
func isPRMergedOrClosed(ctx context.Context, task *devpipelinev1alpha1.DevTask, githubToken string) (bool, error) {
    if task.Status.PRNumber == 0 {
        return false, nil
    }
    parts := strings.SplitN(task.Spec.Repo, "/", 2)
    ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken})
    ghClient := gh.NewClient(oauth2.NewClient(ctx, ts))
    pr, _, err := ghClient.PullRequests.Get(ctx, parts[0], parts[1], task.Status.PRNumber)
    if err != nil {
        return false, err
    }
    return pr.GetMerged() || pr.GetState() == "closed", nil
}
```

- [ ] **Step 2: Add BlockedOnClarification detection**

In the `PhaseRunning` case, after detecting `PodSucceeded`/`PodFailed`, add clarification detection:

```go
// Check for /clarification comment on the issue
if pod.Status.Phase == corev1.PodFailed {
    clarified, err := hasRecentClarificationComment(ctx, task, githubToken)
    if err == nil && clarified {
        // Delete pod and namespace, transition to BlockedOnClarification
        _ = deleteNamespace(ctx, r.Client, task.Status.Namespace)
        task.Status.Phase = devpipelinev1alpha1.PhaseBlockedOnClarification
        task.Status.Message = "agent requested clarification"
        return ctrl.Result{RequeueAfter: time.Minute}, r.Status().Update(ctx, task)
    }
    task.Status.Phase = devpipelinev1alpha1.PhaseFailed
    task.Status.Message = "agent pod failed"
    return ctrl.Result{}, r.Status().Update(ctx, task)
}
```

Add `hasRecentClarificationComment` to `github_poller.go`:

```go
func hasRecentClarificationComment(ctx context.Context, task *devpipelinev1alpha1.DevTask, githubToken string) (bool, error) {
    parts := strings.SplitN(task.Spec.Repo, "/", 2)
    ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken})
    ghClient := gh.NewClient(oauth2.NewClient(ctx, ts))
    comments, _, err := ghClient.Issues.ListComments(ctx, parts[0], parts[1], task.Spec.IssueNumber, nil)
    if err != nil {
        return false, err
    }
    for _, c := range comments {
        if strings.HasPrefix(c.GetBody(), "/clarification:") {
            return true, nil
        }
    }
    return false, nil
}
```

- [ ] **Step 3: Add BlockedOnClarification → Pending transition**

```go
case devpipelinev1alpha1.PhaseBlockedOnClarification:
    githubToken, _, err := readPipelineCredentials(ctx, r.Client)
    if err != nil {
        return ctrl.Result{RequeueAfter: time.Minute}, err
    }
    humanReplied, err := humanRepliedAfterClarification(ctx, task, githubToken)
    if err != nil || !humanReplied {
        return ctrl.Result{RequeueAfter: time.Minute}, err
    }
    // Human answered; restart with a fresh pod on the existing branch
    if err := ensureNamespace(ctx, r.Client, task); err != nil {
        return ctrl.Result{}, err
    }
    creds := struct{ github, anthropic string }{}
    creds.github, creds.anthropic, err = readPipelineCredentials(ctx, r.Client)
    if err != nil {
        return ctrl.Result{}, err
    }
    if err := ensureTaskSecret(ctx, r.Client, task, creds.github, creds.anthropic); err != nil {
        return ctrl.Result{}, err
    }
    if err := ensureNetworkPolicy(ctx, r.Client, task); err != nil {
        return ctrl.Result{}, err
    }
    pod := agentPodResume(task) // see below
    if err := ensurePod(ctx, r.Client, pod); err != nil {
        return ctrl.Result{}, err
    }
    task.Status.Phase = devpipelinev1alpha1.PhaseBuilding
    task.Status.Message = "resuming after clarification"
    return ctrl.Result{RequeueAfter: 30 * time.Second}, r.Status().Update(ctx, task)
```

Add `humanRepliedAfterClarification` to `github_poller.go` (checks that the last comment on the issue is not from `github-actions[bot]` or `claude-bot`):

```go
func humanRepliedAfterClarification(ctx context.Context, task *devpipelinev1alpha1.DevTask, githubToken string) (bool, error) {
    parts := strings.SplitN(task.Spec.Repo, "/", 2)
    ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: githubToken})
    ghClient := gh.NewClient(oauth2.NewClient(ctx, ts))
    comments, _, err := ghClient.Issues.ListComments(ctx, parts[0], parts[1], task.Spec.IssueNumber, nil)
    if err != nil || len(comments) == 0 {
        return false, err
    }
    last := comments[len(comments)-1]
    botNames := []string{"github-actions[bot]", "claude-bot", "app/github-actions"}
    for _, bot := range botNames {
        if last.GetUser().GetLogin() == bot {
            return false, nil
        }
    }
    return true, nil
}
```

Add `agentPodResume` to `pod.go` (same as `agentPod` but with a different prompt that tells the agent to resume from the existing branch):

```go
func agentPodResume(task *devpipelinev1alpha1.DevTask) *corev1.Pod {
    repo := repoName(task.Spec.Repo)
    resumePrompt := fmt.Sprintf(
        "You are resuming work on GitHub issue #%d in %s.\n\n"+
            "The branch claude/issue-%d already exists. Check it out.\n"+
            "Read the latest issue comments — a human has answered your /clarification request.\n"+
            "Continue implementing from where you left off.\n"+
            "When done: run tests, push, update the PR, comment on the issue with the PR URL.",
        task.Spec.IssueNumber, task.Spec.Repo, task.Spec.IssueNumber,
    )

    p := agentPod(task)
    // Replace the init container's script with the resume prompt
    runScript := fmt.Sprintf(
        "#!/bin/bash\nset -e\ncd /workspaces/%s\ngit checkout claude/issue-%d 2>/dev/null || git checkout -b claude/issue-%d\nclaude -p %q "+
            "--allowedTools 'Read,Edit,Write,Bash,mcp__github' "+
            "--dangerously-skip-permissions --output-format json > /tmp/claude-output.json",
        repo, task.Spec.IssueNumber, task.Spec.IssueNumber, resumePrompt,
    )
    p.Spec.InitContainers[0].Env[0].Value = runScript
    return p
}
```

- [ ] **Step 4: Build**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

- [ ] **Step 5: Integration test the full state machine**

Label an issue `ready-for-development`. Start the operator. Watch the DevTask go through:
`Building` → `Running` → `AwaitingReview`

Merge the PR on GitHub. Within 2 minutes:
`AwaitingReview` → `Completed`

Verify the sandbox namespace is deleted:
```bash
kubectl get namespace devtask-<NUMBER>
```
Expected: `Error from server (NotFound)`

- [ ] **Step 6: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/
go.mod go.sum
git commit -m "feat: full state machine with AwaitingReview->Completed and BlockedOnClarification"
```

---

## Exit Criteria

Phase 3 is complete when:

1. Label an issue `ready-for-development` → wait up to 30s → `DevTask` CR created automatically
2. `DevTask` progresses through full state machine to `Completed`
3. Sandbox namespace is deleted after PR merge
4. NetworkPolicy blocks egress to example.com but allows api.github.com
5. Credentials live in per-task Kubernetes Secrets (not env vars)
6. Pod security context: non-root, read-only rootFS, all caps dropped

Move this plan to `docs/plans/archive/` when done.
