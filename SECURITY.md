# Security Policy

This project runs headless AI agents against GitHub issues. Its entire purpose
is to execute code in response to (potentially untrusted) input, so security
reports are taken seriously and prioritized over feature work.

## Reporting a vulnerability

**Do not open a public GitHub issue for security bugs.**

Report privately via GitHub's private vulnerability reporting:
**Security → Report a vulnerability** on this repository
(<https://github.com/imeto-consulting/agentic-dev-pipeline/security/advisories/new>).

Include:

- What an attacker can do (impact), and from which position (issue author,
  commenter, contributor with triage permission, cluster operator).
- Reproduction steps or a proof of concept.
- The commit or release you tested against.

You will get an acknowledgement within 7 days. Fixes for confirmed
vulnerabilities are developed in a private fork and disclosed in the
CHANGELOG/release notes once a patched release is available.

## Threat model (what is in scope)

The pipeline's security boundaries are documented in
[ARCHITECTURE.md → Security Model](ARCHITECTURE.md#security-model). In-scope
report categories, roughly in order of severity:

1. **Sandbox escape** — an agent pod reaching cluster resources outside its
   task namespace (other namespaces' Secrets, the Kubernetes API with
   privileges, the operator).
2. **Credential exfiltration** — a prompt-injected agent leaking the GitHub
   token or the Claude/Anthropic token to an attacker-controlled destination,
   beyond what the documented residual-risk notes already accept.
3. **Supply-chain pivot** — getting the agent to land changes in CI/CD-adjacent
   paths (`.github/`, `.devcontainer/`, `Dockerfile`, dependency manifests)
   past the operator's diff policy.
4. **Authorization bypass** — triggering agent runs, resuming blocked tasks, or
   steering revision cycles without the repository permissions the design
   assumes (triage permission for labels, maintainer for plan approval).

Prompt injection that merely makes an agent produce a low-quality PR (which a
human still reviews behind branch protection) is a known, accepted residual
risk — but injection that *bypasses a control* listed above is in scope.

## Supported versions

Only the latest release / `main` is supported. This is pre-1.0 software; there
are no security backports.
