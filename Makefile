GO_IMAGE   ?= golang:1.26-alpine
NODE_IMAGE ?= node:22-alpine
# Pinned rather than :latest so a theme release cannot change the published
# manual without a commit. Keep it in step with docs/requirements.txt.
DOCS_IMAGE ?= squidfunk/mkdocs-material:9.7.7

# Both images are published to GitHub's registry under the same org that owns
# the source, so the image and the code it was built from have one name and one
# set of permissions. REGISTRY is a variable because an air-gapped site mirrors
# these under its own host and needs to retag them without editing the Makefile.
REGISTRY ?= ghcr.io/kubemg

AGENT_VERSION ?= 0.7.3
AGENT_REPO    ?= $(REGISTRY)/kubemg-agent
AGENT_IMAGE   ?= $(AGENT_REPO):$(AGENT_VERSION)

# The management plane ships as one image — console embedded in the server
# binary. See the root Dockerfile for why one rather than two.
KUBEMG_VERSION ?= 0.7.3
KUBEMG_REPO    ?= $(REGISTRY)/kubemg
KUBEMG_IMAGE   ?= $(KUBEMG_REPO):$(KUBEMG_VERSION)

# The bastion is deployed on hardware the operator chooses, which increasingly
# includes Graviton and Ampere. Same matrix as the agent, same reason.
KUBEMG_PLATFORMS ?= linux/amd64,linux/arm64

# The agent runs on nodes we do not choose. arm64 is no longer an edge case —
# Graviton, Ampere and Apple Silicon dev clusters are all a `kubectl apply`
# away — and an amd64-only image fails there as ImagePullBackOff, which reads
# like a registry problem rather than a missing platform.
AGENT_PLATFORMS ?= linux/amd64,linux/arm64

DOCKER_GO    = docker run --rm -v $(PWD)/backend:/app -v kubemg-go-mod:/go/pkg/mod -v kubemg-go-build:/root/.cache/go-build -w /app $(GO_IMAGE)
DOCKER_AGENT = docker run --rm -v $(PWD)/agent:/app -v kubemg-go-mod:/go/pkg/mod -v kubemg-go-build:/root/.cache/go-build -w /app $(GO_IMAGE)
DOCKER_NODE  = docker run --rm -v $(PWD)/frontend:/app -v kubemg-npm:/root/.npm -w /app $(NODE_IMAGE)

.PHONY: help build test verify manifest-check \
        backend-build backend-test backend-vet backend-tidy \
        agent-build agent-test agent-vet agent-tidy agent-image agent-image-check agent-push \
        frontend-install frontend-build frontend-lint frontend-contrast \
        docs-build docs-serve \
        image image-check image-push \
        up down reset logs ps

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "\033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ---- aggregate ----
build: backend-build agent-build frontend-build ## Build backend + agent + frontend in containers
test: backend-test agent-test ## Run all container-based tests
verify: manifest-check backend-vet backend-test backend-build agent-vet agent-test agent-build frontend-lint frontend-contrast frontend-build docs-build ## Full containerized verification

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

# Single-platform and loaded into the local daemon, which is what a local e2e
# pass needs (`minikube image load`, `docker run`). A multi-platform build
# cannot be `--load`ed at all — the daemon's image store holds one manifest,
# not an index — so publishing is a separate target rather than this one with a
# flag. Override AGENT_PLATFORM to build the other architecture under emulation.
AGENT_PLATFORM ?=
agent-image: ## Build the agent image for the local platform and load it
	docker buildx build --load $(if $(AGENT_PLATFORM),--platform $(AGENT_PLATFORM),) \
		-t $(AGENT_IMAGE) -t $(AGENT_REPO):latest \
		--build-arg VERSION=$(AGENT_VERSION) ./agent

# The agent is the open-source half and is pulled by clusters we do not control,
# so both the pinned tag and :latest have to exist in the registry — and both
# have to be a manifest index covering every architecture those clusters run on.
# buildx builds and pushes in one step on purpose: an index cannot be assembled
# from images sitting in the local daemon, so there is nothing here to build
# first and push afterwards. Verify a published tag with
# `docker buildx imagetools inspect $(AGENT_IMAGE)`.
agent-push: ## Build and push the multi-arch agent image (requires docker login)
	docker buildx build --push --platform $(AGENT_PLATFORMS) \
		-t $(AGENT_IMAGE) -t $(AGENT_REPO):latest \
		--build-arg VERSION=$(AGENT_VERSION) ./agent

# Proves the cross-build for every published platform without needing a
# registry: buildx resolves the whole matrix and discards the result. This is
# what keeps arm64 support from rotting silently between releases.
agent-image-check: ## Build the agent image for every published platform (no output)
	docker buildx build --platform $(AGENT_PLATFORMS) \
		--build-arg VERSION=$(AGENT_VERSION) ./agent

## ---- management plane image ----
# The build context is the repository root because this one image spans both
# modules: the console is built and embedded into the server binary. `.dockerignore`
# is what keeps that context from meaning "upload node_modules and .git".

image: ## Build the management plane image for the local platform and load it
	docker buildx build --load $(if $(KUBEMG_PLATFORM),--platform $(KUBEMG_PLATFORM),) \
		-t $(KUBEMG_IMAGE) -t $(KUBEMG_REPO):latest \
		--build-arg VERSION=$(KUBEMG_VERSION) .

# Same reasoning as agent-push: a manifest index cannot be assembled from images
# sitting in the local daemon, so buildx builds and pushes in one step. Verify a
# published tag with `docker buildx imagetools inspect $(KUBEMG_IMAGE)`.
image-push: ## Build and push the multi-arch management plane image (requires docker login)
	docker buildx build --push --platform $(KUBEMG_PLATFORMS) \
		-t $(KUBEMG_IMAGE) -t $(KUBEMG_REPO):latest \
		--build-arg VERSION=$(KUBEMG_VERSION) .

image-check: ## Build the management plane image for every published platform (no output)
	docker buildx build --platform $(KUBEMG_PLATFORMS) \
		--build-arg VERSION=$(KUBEMG_VERSION) .

## ---- frontend ----
frontend-install: ## Install npm dependencies
	$(DOCKER_NODE) npm ci

frontend-build: ## Type-check and build the frontend
	$(DOCKER_NODE) sh -c "npm ci && npm run build"

frontend-lint: ## Lint the frontend
	$(DOCKER_NODE) sh -c "npm ci && npm run lint"

# The deck's quiet text sat below the WCAG AA floor on the light deck for a whole
# phase, because the dark deck is the default and the numbers lived in a comment.
# This measures them instead, and it is part of verify so the next token edited
# to look better cannot quietly drop below the floor. It needs no dependencies,
# so it does not pay for an npm ci.
frontend-contrast: ## Measure the deck's colour pairings against WCAG
	docker run --rm -v $(PWD)/frontend:/app -w /app $(NODE_IMAGE) node scripts/contrast.mjs

## ---- documentation ----
# The manual is built the same way Read the Docs builds it, in a container, and
# with warnings as errors: a link to a page that has been renamed, or a page
# left out of the nav, is a build failure here rather than a dead link somebody
# finds in the published site. `.readthedocs.yaml` sets fail_on_warning for the
# same reason.
DOCKER_DOCS = docker run --rm -v $(PWD):/docs -w /docs $(DOCS_IMAGE)

docs-build: ## Build the Read the Docs manual (warnings are errors)
	$(DOCKER_DOCS) build --strict --site-dir /tmp/site

docs-serve: ## Serve the manual at http://localhost:8000 with live reload
	docker run --rm -it -v $(PWD):/docs -w /docs -p 8000:8000 $(DOCS_IMAGE) serve --dev-addr 0.0.0.0:8000

## ---- dev environment ----
up: ## Start the dev stack (backend :8080, frontend :5173)
	docker compose up --build -d

down: ## Stop the dev stack
	docker compose down

# `down` deliberately keeps the volumes: the Postgres data, the self-signed
# certificate agents have pinned and the session recordings all have to survive
# a restart. This is the escape hatch for when one of them is the problem —
# most often frontend-node-modules after the host's architecture changed.
reset: ## Stop the dev stack and delete its volumes (data, certs, recordings)
	docker compose down -v

logs: ## Tail dev stack logs
	docker compose logs -f

ps: ## Show dev stack status
	docker compose ps
