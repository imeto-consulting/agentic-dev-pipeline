---
name: envbuilder-devcontainer
description: Build and cache devcontainer images using envbuilder. Use when debugging slow builds, verifying cache hits, or troubleshooting envbuilder pod failures.
type: reference
---

# envbuilder Devcontainer Builds

## What envbuilder does

envbuilder (`ghcr.io/coder/envbuilder:latest`) reads a repo's `.devcontainer/devcontainer.json`, builds the described image (running `postCreateCommand` etc.), and starts the resulting container. It replaces the `devcontainer CLI` for headless/cluster use.

## Key environment variables

| Variable | Description |
|---|---|
| `ENVBUILDER_REPO_URL` | Git repo to clone (e.g. `https://github.com/jonaseck2/slaktforskning`) |
| `ENVBUILDER_CACHE_REPO` | OCI registry for layer cache (e.g. `slaktforskning-registry:5000/slaktforskning-devcontainer`) |
| `ENVBUILDER_PUSH_IMAGE` | `true` to push the built image back to the cache repo |
| `ENVBUILDER_POST_START_SCRIPT_PATH` | Path to a script to run after the container is ready |
| `GIT_USERNAME` / `GIT_PASSWORD` | Auth for private repos (use `GIT_PASSWORD=${GITHUB_TOKEN}`) |

## Cache behaviour

- envbuilder uses the OCI registry as a layer cache
- Cache key = devcontainer content hash
- Cold build: minutes (pulls base image, runs `postCreateCommand`)
- Warm build: seconds (cache hit, just restores layers)
- Cache is shared across all pods in the cluster via the local registry

## Test envbuilder manually

```bash
./scripts/test-envbuilder.sh
# or:
kubectl run envbuilder-test \
  --image=ghcr.io/coder/envbuilder:latest \
  --restart=Never \
  --env="ENVBUILDER_REPO_URL=https://github.com/jonaseck2/slaktforskning" \
  --env="ENVBUILDER_CACHE_REPO=slaktforskning-registry:5000/slaktforskning-devcontainer" \
  --env="ENVBUILDER_PUSH_IMAGE=true" \
  --overrides='{"spec":{"volumes":[{"name":"w","emptyDir":{}}],"containers":[{"name":"envbuilder-test","volumeMounts":[{"name":"w","mountPath":"/workspaces"}]}]}}'

kubectl logs envbuilder-test --follow
kubectl delete pod envbuilder-test
```

## Verify cache hit

```bash
# After a successful build, the image should be in the registry
curl http://localhost:5000/v2/slaktforskning-devcontainer/tags/list

# Run the test again — should complete in <30s
time kubectl run envbuilder-test-2 \
  --image=ghcr.io/coder/envbuilder:latest \
  --restart=Never \
  --env="ENVBUILDER_REPO_URL=https://github.com/jonaseck2/slaktforskning" \
  --env="ENVBUILDER_CACHE_REPO=slaktforskning-registry:5000/slaktforskning-devcontainer"
```

## Troubleshooting

**Build hangs at npm install / pip install:**
This is normal on the first run. `postCreateCommand` installs all dependencies. Check logs:
```bash
kubectl logs envbuilder-test --follow
```

**"failed to push image" error:**
The local registry is not reachable. Verify:
```bash
curl http://slaktforskning-registry:5000/v2/   # from inside cluster
curl http://localhost:5000/v2/                  # from host
```

**"devcontainer.json not found" error:**
envbuilder looks for `.devcontainer/devcontainer.json` at the repo root. Check the repo has it:
```bash
ls /Users/jonasahnstedt/git/slaktforskning/.devcontainer/
```

**Pod starts but claude -p not found:**
The devcontainer doesn't install the `claude` CLI. Add to `Dockerfile` or `devcontainer.json`:
```dockerfile
RUN npm install -g @anthropic-ai/claude-code
```
Or for the devcontainer feature approach, add to `.devcontainer/devcontainer.json`:
```json
"features": {
  "ghcr.io/anthropics/devcontainer-features/claude-code:1": {}
}
```

**ENVBUILDER_POST_START_SCRIPT_PATH not executing:**
The script must be executable and present before envbuilder starts. Use an initContainer to write it:
```yaml
initContainers:
- name: write-script
  image: busybox
  command: ["sh", "-c", "printf '%s' \"$SCRIPT\" > /tmp/run-agent.sh && chmod +x /tmp/run-agent.sh"]
  env:
  - name: SCRIPT
    value: "#!/bin/bash\ncd /workspaces/slaktforskning\nclaude -p ..."
  volumeMounts:
  - name: tmp
    mountPath: /tmp
```
