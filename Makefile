.PHONY: cluster install seed-image secrets run triage demo clean

cluster:
	./scripts/cluster-create.sh

install:
	cd operator && make install
	kubectl apply -k deploy/

# Push the slaktforskning devcontainer image into the in-cluster registry.
# Required before the first triage / agent run on a fresh cluster, because the
# triage CronJob and operator-spawned agent pods both pull this image.
# Re-runs envbuilder via scripts/test-envbuilder.sh if the image isn't cached
# locally; otherwise just pushes the existing local image.
seed-image:
	@if docker image inspect localhost:5050/slaktforskning-devcontainer:latest >/dev/null 2>&1; then \
		echo "Pushing cached devcontainer to in-cluster registry..."; \
		docker push localhost:5050/slaktforskning-devcontainer:latest; \
	else \
		echo "No cached image — building via envbuilder (cold build: several minutes)..."; \
		./scripts/test-envbuilder.sh; \
	fi

secrets:
	@test -n "$(GITHUB_TOKEN)" || (echo "GITHUB_TOKEN not set" && exit 1)
	@test -n "$(CLAUDE_TOKEN)" || (echo "CLAUDE_TOKEN not set" && exit 1)
	@test -n "$(GIT_AUTHOR_NAME)" || (echo "GIT_AUTHOR_NAME not set" && exit 1)
	@test -n "$(GIT_AUTHOR_EMAIL)" || (echo "GIT_AUTHOR_EMAIL not set" && exit 1)
	@kubectl create secret generic pipeline-creds \
		--namespace devpipeline-system \
		--from-literal=github-token="$(GITHUB_TOKEN)" \
		--from-literal=claude-token="$(CLAUDE_TOKEN)" \
		--from-literal=git-author-name="$(GIT_AUTHOR_NAME)" \
		--from-literal=git-author-email="$(GIT_AUTHOR_EMAIL)" \
		--dry-run=client -o yaml | kubectl apply -f -
	@kubectl create secret generic pipeline-creds \
		--namespace agentic-dev-pipeline-triage \
		--from-literal=github-token="$(GITHUB_TOKEN)" \
		--from-literal=claude-token="$(CLAUDE_TOKEN)" \
		--dry-run=client -o yaml | kubectl apply -f -

run:
	cd operator && make run

# Trigger a one-off triage run immediately, instead of waiting for the next
# scheduled CronJob fire (every 5 minutes). The job name is timestamped so it
# never conflicts with previous runs.
triage:
	$(eval JOB := triage-manual-$(shell date +%s))
	kubectl create job --from=cronjob/triage-agent $(JOB) -n agentic-dev-pipeline-triage
	kubectl logs -n agentic-dev-pipeline-triage job/$(JOB) --follow

demo:
	@echo "Filing a demo issue on jonaseck2/slaktforskning..."
	@ISSUE_NUMBER=$$(gh issue create \
		--repo jonaseck2/slaktforskning \
		--title "Demo: add birth-year range filter to search_persons" \
		--label "needs-triage" \
		--body "The search_persons MCP tool should accept optional birth_year_min and birth_year_max parameters. When provided, only return persons whose birth year is within the given range. When omitted, return all results as today." \
		| grep -oE '[0-9]+$$') && \
	echo "Issue #$${ISSUE_NUMBER} filed." && \
	echo "Watch triage: kubectl create job --from=cronjob/triage-agent triage-demo -n agentic-dev-pipeline-triage" && \
	echo "Watch DevTask: kubectl get devtask -n devpipeline-system --watch"

clean:
	k3d cluster delete slaktforskning-poc
