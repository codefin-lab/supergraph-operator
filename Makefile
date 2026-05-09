SHELL := /bin/bash

# ── Config ────────────────────────────────────────────
APP_NAME       := supergraph-operator
IMAGE_NAME     := ghcr.io/codefin/$(APP_NAME)
IMAGE_TAG      ?= latest
CHART_DIR      := charts/$(APP_NAME)
RELEASE_NAME   := $(APP_NAME)
ENV            ?= local
NAMESPACE      ?= $(shell grep '^namespace:' $(CHART_DIR)/values-$(ENV).yaml 2>/dev/null | awk '{print $$2}' || echo vahalla)

# ── Targets ───────────────────────────────────────────
.PHONY: help build test run docker-build docker-save deploy upgrade \
        k8s-restart redeploy template clean generate

help: ## Show available targets
	@echo 'Usage: make [target] ENV=local|dev|demo|prod'
	@echo ''
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ── Build ─────────────────────────────────────────────

build: ## Build the controller binary
	@echo "Building $(APP_NAME)..."
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(APP_NAME) ./cmd/main.go
	@echo "✅ Built bin/$(APP_NAME)"

test: ## Run tests
	@echo "Running tests..."
	go test ./... -v -count=1
	@echo "✅ Tests passed"

run: build ## Run locally (requires kubeconfig)
	./bin/$(APP_NAME) --namespace=$(NAMESPACE)

# ── Docker ────────────────────────────────────────────

docker-build: ## Build Docker image
	@echo "Building Docker image $(IMAGE_NAME):$(IMAGE_TAG)..."
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .
	@echo "✅ Docker image built"

docker-save: docker-build ## Build + load image into kind/k3d/Docker Desktop
	@echo "Image $(IMAGE_NAME):$(IMAGE_TAG) ready (local daemon)"

# ── Helm ──────────────────────────────────────────────

template: ## Dry-run: render Helm templates (ENV=local|dev|demo|prod)
	helm template $(RELEASE_NAME) $(CHART_DIR) \
		-f $(CHART_DIR)/values-$(ENV).yaml \
		-n $(NAMESPACE)

deploy: docker-save ## Build image + install/upgrade Helm chart (ENV=local|dev|demo|prod)
	helm upgrade --install $(RELEASE_NAME) $(CHART_DIR) \
		-f $(CHART_DIR)/values-$(ENV).yaml \
		-n $(NAMESPACE) \
		--create-namespace \
		--timeout 5m \
		--debug
	@echo "✅ $(APP_NAME) deployed to $(ENV)"

upgrade: docker-save ## Upgrade Helm release (ENV=local|dev|demo|prod)
	helm upgrade --install $(RELEASE_NAME) $(CHART_DIR) \
		-f $(CHART_DIR)/values-$(ENV).yaml \
		-n $(NAMESPACE) \
		--timeout 5m \
		--debug
	@echo "✅ $(APP_NAME) upgraded"

k8s-restart: ## Restart controller pod
	kubectl rollout restart deployment/$(RELEASE_NAME) -n $(NAMESPACE)

redeploy: upgrade k8s-restart ## Upgrade + restart

# ── Code Generation ───────────────────────────────────

generate: ## Generate CRD manifests and deepcopy
	@echo "Generating CRD manifests..."
	@echo "Note: Run 'go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest' first"
	controller-gen crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	controller-gen object paths="./api/..."
	@echo "✅ CRD manifests generated"

# ── Cleanup ───────────────────────────────────────────

clean: ## Remove built artifacts
	rm -rf bin/
	@echo "✅ Cleaned"
