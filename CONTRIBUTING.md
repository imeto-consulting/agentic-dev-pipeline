# Contributing

Thanks for your interest in the agentic-dev-pipeline. This is a small,
security-sensitive codebase — please read this page before opening a PR.

## Ground rules

- **Security reports go through [SECURITY.md](SECURITY.md), never public issues.**
- All commits must be signed off (DCO): `git commit -s`. The agents in this
  pipeline do the same; humans aren't exempt.
- One concern per PR. The repo's own triage/impl agents work in single-issue
  units, and human PRs should too.

## Development setup

Prerequisites: Docker, [k3d](https://k3d.io), `kubectl`, `gh` (authenticated),
Go ≥ 1.25, GNU make, `envsubst` (gettext).

```bash
make init          # writes .pipeline.env (target repo, cluster/registry names)
make cluster       # k3d cluster with Calico CNI (NetworkPolicy enforcement)
make install       # CRD + namespaces + triage CronJob manifests
make seed-image    # build + push the devcontainer image to the in-cluster registry
make secrets       # store GitHub + Claude credentials in the cluster
make run           # run the operator locally against the cluster
```

`make demo` files a demo issue on the target repo; `make triage` fires a
one-off triage job. See [README.md](README.md) and
[ARCHITECTURE.md](ARCHITECTURE.md) for how the pieces fit.

## Testing

Operator changes must pass:

```bash
make -C operator test   # unit + envtest
make -C operator lint   # golangci-lint
```

Pure-logic changes (e.g. the diff policy) need table-driven unit tests next to
the code. Changes to pod specs, NetworkPolicies, or prompts should state in the
PR body how they were verified against a live cluster (`make demo` end-to-end
is the reference check).

Manifest changes: render them the way `make install` does before pushing —

```bash
envsubst '$TARGET_REPO $REGISTRY_NAME $DEVCONTAINER_IMAGE' < deploy/triage/cronjob.yaml | kubectl apply --dry-run=client -f -
```

## Security-sensitive paths

Changes under `operator/internal/controller/` (pod specs, NetworkPolicy, diff
policy, token handling), `deploy/`, and the agent prompts get extra scrutiny:
explain in the PR body what an attacker who controls issue text could do with
your change. PRs that weaken the sandbox or widen credential scope need a
documented justification in [ARCHITECTURE.md → Security Model](ARCHITECTURE.md#security-model).

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), and the DCO sign-off certifies you have the
right to submit them.
