# POC Phase 2: k3d Cluster, CRD, Minimal Operator

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move from "agent runs in a devcontainer I opened by hand" to "pod the cluster spins up in response to a CR." `kubectl apply -f devtask.yaml` → PR appears on `slaktforskning`.

**Architecture:** k3d single-node cluster with Calico CNI. kubebuilder operator in `operator/`. On `DevTask` creation, the controller creates a namespace + envbuilder pod that builds the `slaktforskning` devcontainer and runs `claude -p`.

**Tech Stack:** k3d, Calico, kubebuilder v3, Go 1.22+, envbuilder (`ghcr.io/coder/envbuilder`), k3d local registry

**Prerequisite:** Phase 1 complete. Working prompt template in `config/agent-prompt-v1.txt`. Labels exist on `slaktforskning` repo.

---

### Task 1: Create the k3d cluster with Calico

**Files:**
- Create: `scripts/cluster-create.sh`

- [ ] **Step 1: Create the cluster script**

```bash
mkdir -p /Users/jonasahnstedt/git/agentic-dev-pipeline/scripts
```

Create `scripts/cluster-create.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

CLUSTER_NAME=slaktforskning-poc
REGISTRY_NAME=slaktforskning-registry
REGISTRY_PORT=5000

echo "Creating k3d cluster: ${CLUSTER_NAME}"
k3d cluster create "${CLUSTER_NAME}" \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create "${REGISTRY_NAME}:${REGISTRY_PORT}" \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

kubectl wait --for=condition=Ready nodes --all --timeout=60s

echo "Installing Calico CNI..."
kubectl create -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/tigera-operator.yaml
kubectl create -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/custom-resources.yaml

kubectl wait --for=condition=Available deployment/calico-kube-controllers \
  -n calico-system --timeout=180s

echo "Cluster ready. Registry at localhost:${REGISTRY_PORT}"
```

```bash
chmod +x /Users/jonasahnstedt/git/agentic-dev-pipeline/scripts/cluster-create.sh
```

- [ ] **Step 2: Run the cluster creation script**

```bash
/Users/jonasahnstedt/git/agentic-dev-pipeline/scripts/cluster-create.sh
```

Expected: `kubectl get nodes` shows 2 nodes in Ready state.

- [ ] **Step 3: Verify Calico enforces NetworkPolicy**

```bash
kubectl run test-sender --image=busybox --restart=Never -- sleep 3600
kubectl run test-receiver --image=busybox --restart=Never -- sleep 3600
kubectl wait --for=condition=Ready pod/test-sender pod/test-receiver --timeout=60s

kubectl apply -f - << 'NETPOL'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-all
  namespace: default
spec:
  podSelector:
    matchLabels:
      run: test-receiver
  policyTypes: [Ingress, Egress]
NETPOL

RECEIVER_IP=$(kubectl get pod test-receiver -o jsonpath='{.status.podIP}')
kubectl exec test-sender -- timeout 3 ping -c 1 "${RECEIVER_IP}" && echo "FAIL: traffic allowed" || echo "PASS: traffic blocked"

kubectl delete pod test-sender test-receiver
kubectl delete networkpolicy deny-all
```

Expected: `PASS: traffic blocked`

- [ ] **Step 4: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add scripts/cluster-create.sh
git commit -m "feat: k3d cluster creation script with Calico"
```

---

### Task 2: Scaffold the kubebuilder operator

**Files:**
- Create: `operator/` (entire directory, kubebuilder scaffold)

- [ ] **Step 1: Scaffold**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
mkdir operator && cd operator

kubebuilder init \
  --domain devpipeline.local \
  --repo github.com/jonaseck2/agentic-dev-pipeline/operator

kubebuilder create api \
  --group devpipeline \
  --version v1alpha1 \
  --kind DevTask \
  --resource \
  --controller
```

Answer `y` to both resource and controller prompts.

- [ ] **Step 2: Define DevTask types**

Edit `operator/api/v1alpha1/devtask_types.go`. Replace the Spec and Status structs:

```go
// DevTaskSpec defines the desired state of DevTask
type DevTaskSpec struct {
    // IssueNumber is the GitHub issue number to implement
    // +kubebuilder:validation:Minimum=1
    IssueNumber int `json:"issueNumber"`

    // Repo is the GitHub repository in "owner/name" format
    // +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`
    Repo string `json:"repo"`
}

// DevTaskPhase represents the current lifecycle phase
// +kubebuilder:validation:Enum=Pending;Building;Running;AwaitingReview;BlockedOnClarification;Failed;Completed
type DevTaskPhase string

const (
    PhasePending                DevTaskPhase = "Pending"
    PhaseBuilding               DevTaskPhase = "Building"
    PhaseRunning                DevTaskPhase = "Running"
    PhaseAwaitingReview         DevTaskPhase = "AwaitingReview"
    PhaseBlockedOnClarification DevTaskPhase = "BlockedOnClarification"
    PhaseFailed                 DevTaskPhase = "Failed"
    PhaseCompleted              DevTaskPhase = "Completed"
)

// DevTaskStatus defines the observed state of DevTask
type DevTaskStatus struct {
    // +optional
    Phase DevTaskPhase `json:"phase,omitempty"`
    // +optional
    Namespace string `json:"namespace,omitempty"`
    // +optional
    PRNumber int `json:"prNumber,omitempty"`
    // +optional
    StartedAt *metav1.Time `json:"startedAt,omitempty"`
    // +optional
    Message string `json:"message,omitempty"`
}
```

Add markers above the `DevTask` struct:

```go
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Issue",type="integer",JSONPath=".spec.issueNumber"
// +kubebuilder:printcolumn:name="PR",type="integer",JSONPath=".status.prNumber"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
```

- [ ] **Step 3: Regenerate**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
make generate && make manifests && go build ./...
```

Expected: No errors. `config/crd/bases/devpipeline.local_devtasks.yaml` updated.

- [ ] **Step 4: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/
git commit -m "feat: kubebuilder scaffold with DevTask CRD"
```

---

### Task 3: Implement the minimal controller

**Files:**
- Modify: `operator/internal/controller/devtask_controller.go`
- Create: `operator/internal/controller/namespace.go`
- Create: `operator/internal/controller/pod.go`

- [ ] **Step 1: Create `operator/internal/controller/namespace.go`**

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

func taskNamespace(task *devpipelinev1alpha1.DevTask) string {
    return fmt.Sprintf("devtask-%d", task.Spec.IssueNumber)
}

func ensureNamespace(ctx context.Context, c client.Client, task *devpipelinev1alpha1.DevTask) error {
    ns := &corev1.Namespace{
        ObjectMeta: metav1.ObjectMeta{
            Name: taskNamespace(task),
            Labels: map[string]string{
                "app.kubernetes.io/managed-by": "agentic-dev-pipeline",
                "devpipeline.local/task":       task.Name,
            },
        },
    }
    return client.IgnoreAlreadyExists(c.Create(ctx, ns))
}

func deleteNamespace(ctx context.Context, c client.Client, ns string) error {
    obj := &corev1.Namespace{}
    obj.Name = ns
    return client.IgnoreNotFound(c.Delete(ctx, obj))
}
```

- [ ] **Step 2: Create `operator/internal/controller/pod.go`**

```go
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
    registryBase    = "slaktforskning-registry:5000"
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
            "4. Push. Open a PR against main. Comment on the issue with the PR URL.\n\n"+
            "If blocked: commit WIP, push, open a draft PR, comment '/clarification:' on the issue, exit 2.",
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
                    {Name: "GITHUB_TOKEN", Value: githubToken},
                    {Name: "ANTHROPIC_API_KEY", Value: anthropicKey},
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
```

- [ ] **Step 3: Implement Reconcile in `devtask_controller.go`**

Replace the placeholder `Reconcile` body:

```go
func (r *DevTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    logger := log.FromContext(ctx)

    task := &devpipelinev1alpha1.DevTask{}
    if err := r.Get(ctx, req.NamespacedName, task); err != nil {
        return ctrl.Result{}, client.IgnoreNotFound(err)
    }

    switch task.Status.Phase {
    case "":
        // New task: create namespace and agent pod
        logger.Info("New DevTask", "issue", task.Spec.IssueNumber)
        if err := ensureNamespace(ctx, r.Client, task); err != nil {
            return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
        }
        pod := agentPod(task, os.Getenv("GITHUB_TOKEN"), os.Getenv("ANTHROPIC_API_KEY"))
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
        // Terminal states: nothing to do
        return ctrl.Result{}, nil
    }

    return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}
```

Add imports: `"fmt"`, `"os"`, `"time"`, `corev1 "k8s.io/api/core/v1"`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`.

- [ ] **Step 4: Build**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
go build ./...
```

Fix any errors before continuing.

- [ ] **Step 5: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add operator/internal/controller/
git commit -m "feat: minimal controller creates namespace+pod on new DevTask"
```

---

### Task 4: Test envbuilder builds the slaktforskning devcontainer

**Files:**
- Create: `scripts/test-envbuilder.sh`

- [ ] **Step 1: Create test script**

Create `scripts/test-envbuilder.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

kubectl run envbuilder-test \
  --image=ghcr.io/coder/envbuilder:latest \
  --restart=Never \
  --env="ENVBUILDER_REPO_URL=https://github.com/jonaseck2/slaktforskning" \
  --env="ENVBUILDER_CACHE_REPO=slaktforskning-registry:5000/slaktforskning-devcontainer" \
  --env="ENVBUILDER_PUSH_IMAGE=true" \
  --overrides='{"spec":{"volumes":[{"name":"w","emptyDir":{}}],"containers":[{"name":"envbuilder-test","volumeMounts":[{"name":"w","mountPath":"/workspaces"}]}]}}'

echo "Following envbuilder-test logs (cold build: several minutes)..."
kubectl wait --for=condition=Ready pod/envbuilder-test --timeout=600s || true
kubectl logs envbuilder-test --follow || true

kubectl get pod envbuilder-test -o jsonpath='{.status.phase}'
kubectl delete pod envbuilder-test --ignore-not-found
```

```bash
chmod +x /Users/jonasahnstedt/git/agentic-dev-pipeline/scripts/test-envbuilder.sh
```

- [ ] **Step 2: Run it**

```bash
/Users/jonasahnstedt/git/agentic-dev-pipeline/scripts/test-envbuilder.sh
```

Expected: `Succeeded`. Warm rerun should complete in under 30 seconds.

- [ ] **Step 3: Verify registry has the image**

```bash
curl http://localhost:5000/v2/slaktforskning-devcontainer/tags/list
```

Expected: JSON response with tags.

- [ ] **Step 4: Commit**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add scripts/test-envbuilder.sh
git commit -m "chore: envbuilder smoke-test script"
```

---

### Task 5: Run the full flow end-to-end

**Files:**
- Create: `config/samples/devtask-sample.yaml`

- [ ] **Step 1: Install CRD and start operator**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
make install
kubectl create namespace devpipeline-system --dry-run=client -o yaml | kubectl apply -f -
```

In a separate terminal:

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline/operator
export GITHUB_TOKEN=<your-token>
export ANTHROPIC_API_KEY=<your-key>
make run
```

- [ ] **Step 2: Create sample DevTask**

```bash
cat > /Users/jonasahnstedt/git/agentic-dev-pipeline/config/samples/devtask-sample.yaml << 'YAML'
apiVersion: devpipeline.local/v1alpha1
kind: DevTask
metadata:
  name: slaktforskning-test
  namespace: devpipeline-system
spec:
  issueNumber: 1
  repo: jonaseck2/slaktforskning
YAML
```

Edit `issueNumber` to match the Phase 1 test issue. Then:

```bash
kubectl apply -f /Users/jonasahnstedt/git/agentic-dev-pipeline/config/samples/devtask-sample.yaml
```

- [ ] **Step 3: Watch progress**

```bash
# Terminal 1: watch DevTask phases
watch kubectl get devtask -n devpipeline-system

# Terminal 2: once namespace is created, watch pod
NS=devtask-<ISSUE-NUMBER>
kubectl logs -n "${NS}" agent --follow
```

Expected sequence: Phase `Building` → `Running` → `AwaitingReview`

- [ ] **Step 4: Verify PR**

```bash
gh pr list --repo jonaseck2/slaktforskning --state open
```

Expected: PR from `claude/issue-<NUMBER>` is open.

- [ ] **Step 5: Commit sample**

```bash
cd /Users/jonasahnstedt/git/agentic-dev-pipeline
git add config/samples/devtask-sample.yaml
git commit -m "chore: sample DevTask"
```

---

## Exit Criteria

Phase 2 is complete when:

1. `kubectl apply -f config/samples/devtask-sample.yaml` triggers a real PR on `slaktforskning`
2. envbuilder cache works (warm rebuild is noticeably faster)
3. `kubectl get devtask` shows Phase/Issue/PR columns
4. Calico NetworkPolicy blocks traffic as expected

Move this plan to `docs/plans/archive/` when done.
