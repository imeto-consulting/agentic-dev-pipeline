---
name: k3d-cluster-ops
description: Create, inspect, and tear down the agentic-dev-pipeline k3d cluster with Calico. Use when setting up the cluster, troubleshooting networking, or verifying NetworkPolicy enforcement.
type: reference
---

# k3d Cluster Operations

## Cluster name and registry

- Cluster: `slaktforskning-poc`
- Local registry: `slaktforskning-registry:5000` (accessible as `localhost:5000` from the host)

## Create

```bash
./scripts/cluster-create.sh
# or manually:
k3d cluster create slaktforskning-poc \
  --agents 1 \
  --port "8080:80@loadbalancer" \
  --registry-create slaktforskning-registry:5000 \
  --k3s-arg "--flannel-backend=none@server:*" \
  --k3s-arg "--disable-network-policy@server:*"

kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/tigera-operator.yaml
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/custom-resources.yaml
kubectl wait --for=condition=Available deployment/calico-kube-controllers -n calico-system --timeout=180s
```

## Destroy

```bash
k3d cluster delete slaktforskning-poc
```

## Common operations

```bash
# List clusters
k3d cluster list

# Get kubeconfig
k3d kubeconfig get slaktforskning-poc

# List nodes
kubectl get nodes

# Check Calico is running
kubectl get pods -n calico-system
```

## Verify NetworkPolicy enforcement

Calico must be running for policies to be enforced. k3d's default Flannel ignores NetworkPolicy.

```bash
# Quick test: deploy two pods, apply deny policy, verify traffic is blocked
kubectl run sender --image=busybox --restart=Never -- sleep 3600
kubectl run receiver --image=busybox --restart=Never -- sleep 3600
kubectl wait --for=condition=Ready pod/sender pod/receiver --timeout=60s

kubectl apply -f - <<'NETPOL'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-receiver
  namespace: default
spec:
  podSelector:
    matchLabels:
      run: receiver
  policyTypes: [Ingress, Egress]
NETPOL

RECEIVER_IP=$(kubectl get pod receiver -o jsonpath='{.status.podIP}')
kubectl exec sender -- timeout 3 ping -c 1 "${RECEIVER_IP}" && echo "FAIL: not enforcing" || echo "PASS: enforcing"

# Cleanup
kubectl delete pod sender receiver
kubectl delete networkpolicy deny-receiver
```

## Registry operations

```bash
# List images in the local registry
curl http://localhost:5000/v2/_catalog

# List tags for a specific image
curl http://localhost:5000/v2/slaktforskning-devcontainer/tags/list

# Push an image to the local registry
docker tag myimage:latest localhost:5000/myimage:latest
docker push localhost:5000/myimage:latest
```

## Troubleshooting

**Pods stuck in Pending after cluster create:**
Calico is still initializing. Wait 2 minutes and check:
```bash
kubectl get pods -n calico-system
```

**NetworkPolicy not working:**
Check Calico is the CNI (not Flannel):
```bash
kubectl get pods -n calico-system | grep calico-node
```
If no calico-node pods, the cluster was created without the `--flannel-backend=none` flag. Recreate it.

**Registry unreachable from pods:**
The k3d registry is accessible as `slaktforskning-registry:5000` from inside the cluster (k3d adds it to `/etc/hosts` in the nodes). From the host machine it's `localhost:5000`. Make sure `ENVBUILDER_CACHE_REPO` uses the in-cluster hostname.
