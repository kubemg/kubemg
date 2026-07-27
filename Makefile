GO_IMAGE   ?= golang:1.26-alpine
NODE_IMAGE ?= node:22-alpine

AGENT_VERSION ?= 0.2.0
AGENT_REPO    ?= docker.io/ozkanpoyrazoglu/kubemg-agent
AGENT_IMAGE   ?= $(AGENT_REPO):$(AGENT_VERSION)

DOCKER_GO    = docker run --rm -v $(PWD)/backend:/app -v kubemg-go-mod:/go/pkg/mod -v kubemg-go-build:/root/.cache/go-build -w /app $(GO_IMAGE)
DOCKER_AGENT = docker run --rm -v $(PWD)/agent:/app -v kubemg-go-mod:/go/pkg/mod -v kubemg-go-build:/root/.cache/go-build -w /app $(GO_IMAGE)
DOCKER_NODE  = docker run --rm -v $(PWD)/frontend:/app -v kubemg-npm:/root/.npm -w /app $(NODE_IMAGE)

.PHONY: help build test verify manifest-check \
        backend-build backend-test backend-vet backend-tidy \
        agent-build agent-test agent-vet agent-tidy agent-image agent-push \
        frontend-install frontend-build frontend-lint \
        up down logs ps

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ---- aggregate ----
build: backend-build agent-build frontend-build ## Build backend + agent + frontend in containers
test: backend-test agent-test ## Run all container-based tests
verify: manifest-check backend-vet backend-test backend-build agent-vet agent-test agent-build frontend-lint frontend-build ## Full containerized verification

# The bastion embeds its own copy of the agent manifests so they ship inside the
# server binary. Two copies can drift; this makes drift a build failure.
manifest-check: ## Verify the embedded agent manifests match deploy/kustomize/base
	@docker run --rm -v $(PWD):/repo -w /repo $(GO_IMAGE) \
		diff -ru deploy/kustomize/base backend/pkg/agentpkg/base \
		|| { echo "deploy/kustomize/base and backend/pkg/agentpkg/base have drifted; update both."; exit 1; }

## ---- backend ----
backend-build: ## Compile the Go server binary
	$(DOCKER_GO) go build -o server ./cmd/server

backend-test: ## Run Go tests
	$(DOCKER_GO) go test ./...

backend-vet: ## Run go vet
	$(DOCKER_GO) go vet ./...

backend-tidy: ## Run go mod tidy
	$(DOCKER_GO) go mod tidy

## ---- agent (open source) ----
agent-build: ## Compile the static agent binary
	$(DOCKER_AGENT) sh -c "CGO_ENABLED=0 go build -ldflags '-s -w -X main.version=$(AGENT_VERSION)' -o kubemg-agent ./cmd/agent"

agent-test: ## Run agent tests
	$(DOCKER_AGENT) go test ./...

agent-vet: ## Run go vet on the agent
	$(DOCKER_AGENT) go vet ./...

agent-tidy: ## Run go mod tidy on the agent
	$(DOCKER_AGENT) go mod tidy

agent-image: ## Build the agent container image
	docker build -t $(AGENT_IMAGE) -t $(AGENT_REPO):latest --build-arg VERSION=$(AGENT_VERSION) ./agent

# The agent is the open-source half and is pulled by clusters we do not control,
# so both the pinned tag and :latest have to exist in the registry.
agent-push: agent-image ## Push the agent image (requires docker login)
	docker push $(AGENT_IMAGE)
	docker push $(AGENT_REPO):latest

## ---- frontend ----
frontend-install: ## Install npm dependencies
	$(DOCKER_NODE) npm ci

frontend-build: ## Type-check and build the frontend
	$(DOCKER_NODE) sh -c "npm ci && npm run build"

frontend-lint: ## Lint the frontend
	$(DOCKER_NODE) sh -c "npm ci && npm run lint"

## ---- dev environment ----
up: ## Start the dev stack (backend :8080, frontend :5173)
	docker compose up --build -d

down: ## Stop the dev stack
	docker compose down

logs: ## Tail dev stack logs
	docker compose logs -f

ps: ## Show dev stack status
	docker compose ps
