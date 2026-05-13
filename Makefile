.PHONY: init check-config cluster install seed-image secrets run triage demo clean

# Source local config if it exists. Targets that need it call `check-config` first.
-include .pipeline.env
export

# ----------------------------------------------------------------------------
# Onboarding: interactive config writer
# ----------------------------------------------------------------------------

init:
	@if [ -f .pipeline.env ]; then \
		printf ".pipeline.env already exists. Overwrite? [y/N] "; \
		read ans; [ "$$ans" = "y" ] || [ "$$ans" = "Y" ] || (echo "Aborted." && exit 1); \
	fi
	@echo "Configuring pipeline for a new target repo. Press enter to accept defaults."
	@read -p "Target repo (owner/name): " repo; \
	read -p "Cluster name [$${repo##*/}-poc]: " cluster; cluster=$${cluster:-$${repo##*/}-poc}; \
	read -p "Registry name [$${repo##*/}-registry]: " registry; registry=$${registry:-$${repo##*/}-registry}; \
	read -p "Devcontainer image [$${repo##*/}-devcontainer]: " image; image=$${image:-$${repo##*/}-devcontainer}; \
	{ \
		echo "TARGET_REPO=$$repo"; \
		echo "CLUSTER_NAME=$$cluster"; \
		echo "REGISTRY_NAME=$$registry"; \
		echo "DEVCONTAINER_IMAGE=$$image"; \
	} > .pipeline.env
	@echo "Wrote .pipeline.env. Next: make cluster && make seed-image && make secrets && make run"

check-config:
	@test -f .pipeline.env || (echo "Error: .pipeline.env not found. Run 'make init' first." && exit 1)
	@test -n "$(TARGET_REPO)"        || (echo "Error: TARGET_REPO not set in .pipeline.env"        && exit 1)
	@test -n "$(CLUSTER_NAME)"       || (echo "Error: CLUSTER_NAME not set in .pipeline.env"       && exit 1)
	@test -n "$(REGISTRY_NAME)"      || (echo "Error: REGISTRY_NAME not set in .pipeline.env"      && exit 1)
	@test -n "$(DEVCONTAINER_IMAGE)" || (echo "Error: DEVCONTAINER_IMAGE not set in .pipeline.env" && exit 1)
	@case "$(TARGET_REPO)" in */*) ;; *) echo "Error: TARGET_REPO must be in 'owner/name' format" && exit 1 ;; esac

# ----------------------------------------------------------------------------
# Cluster lifecycle
# ----------------------------------------------------------------------------

cluster: check-config
	./scripts/cluster-create.sh

install: check-config
	cd operator && make install
	# Static manifests (no parameterization needed)
	kubectl apply -f deploy/system/namespace.yaml
	kubectl apply -f deploy/triage/namespace.yaml
	kubectl apply -f deploy/triage/networkpolicy.yaml
	kubectl apply -f deploy/triage/rbac.yaml
	# Templated manifests — substitute only our config vars; leave shell ${VAR}
	# references inside scripts untouched.
	envsubst '$$TARGET_REPO $$REGISTRY_NAME $$DEVCONTAINER_IMAGE' \
		< deploy/triage/configmap-prompt.yaml | kubectl apply -f -
	envsubst '$$TARGET_REPO $$REGISTRY_NAME $$DEVCONTAINER_IMAGE' \
		< deploy/triage/cronjob.yaml | kubectl apply -f -

# Push the devcontainer image into the in-cluster registry. Required before the
# first triage / agent run, because the triage CronJob and operator-spawned agent
# pods both pull this image. Re-runs envbuilder via scripts/test-envbuilder.sh
# if the image isn't cached locally; otherwise just pushes the existing image.
seed-image: check-config
	@if docker image inspect localhost:5050/$(DEVCONTAINER_IMAGE):latest >/dev/null 2>&1; then \
		echo "Pushing cached devcontainer to in-cluster registry..."; \
		docker push localhost:5050/$(DEVCONTAINER_IMAGE):latest; \
	else \
		echo "No cached image — building via envbuilder (cold build: several minutes)..."; \
		./scripts/test-envbuilder.sh; \
	fi

# ----------------------------------------------------------------------------
# Secrets: store auth + git identity in the cluster
# ----------------------------------------------------------------------------

secrets: check-config
	@test -n "$(GITHUB_TOKEN)"      || (echo "GITHUB_TOKEN not set"      && exit 1)
	@test -n "$(GIT_AUTHOR_NAME)"   || (echo "GIT_AUTHOR_NAME not set"   && exit 1)
	@test -n "$(GIT_AUTHOR_EMAIL)"  || (echo "GIT_AUTHOR_EMAIL not set"  && exit 1)
	@if [ -n "$(CLAUDE_OAUTH_TOKEN)" ]; then \
		echo "Auth mode: subscription (OAuth token)"; \
		kubectl create secret generic pipeline-creds \
			--namespace devpipeline-system \
			--from-literal=github-token="$(GITHUB_TOKEN)" \
			--from-literal=claude-token="$(CLAUDE_OAUTH_TOKEN)" \
			--from-literal=claude-auth-mode="oauth" \
			--from-literal=git-author-name="$(GIT_AUTHOR_NAME)" \
			--from-literal=git-author-email="$(GIT_AUTHOR_EMAIL)" \
			--dry-run=client -o yaml | kubectl apply -f -; \
		kubectl create secret generic pipeline-creds \
			--namespace agentic-dev-pipeline-triage \
			--from-literal=github-token="$(GITHUB_TOKEN)" \
			--from-literal=claude-token="$(CLAUDE_OAUTH_TOKEN)" \
			--from-literal=claude-auth-mode="oauth" \
			--dry-run=client -o yaml | kubectl apply -f -; \
	elif [ -n "$(CLAUDE_TOKEN)" ]; then \
		echo "Auth mode: API key"; \
		kubectl create secret generic pipeline-creds \
			--namespace devpipeline-system \
			--from-literal=github-token="$(GITHUB_TOKEN)" \
			--from-literal=claude-token="$(CLAUDE_TOKEN)" \
			--from-literal=claude-auth-mode="api" \
			--from-literal=git-author-name="$(GIT_AUTHOR_NAME)" \
			--from-literal=git-author-email="$(GIT_AUTHOR_EMAIL)" \
			--dry-run=client -o yaml | kubectl apply -f -; \
		kubectl create secret generic pipeline-creds \
			--namespace agentic-dev-pipeline-triage \
			--from-literal=github-token="$(GITHUB_TOKEN)" \
			--from-literal=claude-token="$(CLAUDE_TOKEN)" \
			--from-literal=claude-auth-mode="api" \
			--dry-run=client -o yaml | kubectl apply -f -; \
	else \
		echo "Error: set either CLAUDE_OAUTH_TOKEN (subscription) or CLAUDE_TOKEN (API key)" && exit 1; \
	fi

# ----------------------------------------------------------------------------
# Operator
# ----------------------------------------------------------------------------

# Operator picks up TARGET_REPO + image refs from env (sourced from .pipeline.env above).
run: check-config
	cd operator && PIPELINE_REPOS="$(TARGET_REPO)" \
		AGENT_IMAGE="$(REGISTRY_NAME):5000/$(DEVCONTAINER_IMAGE):latest" \
		make run

# Trigger a one-off triage run immediately, instead of waiting for the next
# scheduled CronJob fire (every 5 minutes). The job name is timestamped so it
# never conflicts with previous runs.
triage:
	$(eval JOB := triage-manual-$(shell date +%s))
	kubectl create job --from=cronjob/triage-agent $(JOB) -n agentic-dev-pipeline-triage
	kubectl logs -n agentic-dev-pipeline-triage job/$(JOB) --follow

demo: check-config
	@echo "Filing a demo issue on $(TARGET_REPO)..."
	@ISSUE_NUMBER=$$(gh issue create \
		--repo $(TARGET_REPO) \
		--title "Demo: add a hello-world endpoint" \
		--label "needs-triage" \
		--body "Add a /hello endpoint that returns the string 'hello'." \
		| grep -oE '[0-9]+$$') && \
	echo "Issue #$${ISSUE_NUMBER} filed." && \
	echo "Watch triage: make triage" && \
	echo "Watch DevTask: kubectl get devtask -n devpipeline-system --watch"

clean: check-config
	k3d cluster delete $(CLUSTER_NAME)
