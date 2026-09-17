SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
MAKEFLAGS += --no-print-directory

GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/sdk/go1.26.1/bin/go)
BIN := bin
CMD := mc-agents-operator
VERSION ?= $(shell cat VERSION)

CHART := charts/mc-agents-operator
IMAGE ?= junhyung.cloud/mc-agents/operator
K3D_IMAGE := mc-agents/operator:dev

# A dedicated cluster. hyperfarm-local is shared with other sessions and has already lost work
# to a concurrent deploy, so this refuses to touch it.
K3D_CLUSTER ?= mc-agents
K3D_CONTEXT := k3d-$(K3D_CLUSTER)
NAMESPACE ?= mc-agents-system
# Where the verify run puts its MCPServer, profiles and bots: a tenant, never the operator's own.
TENANT ?= mc-agents-verify
# The published release the upgrade verification starts from: the newest VERSION below this one
# in the history, unless given.
FROM ?= $(shell bash hack/previous-version.sh)

.PHONY: all
all: build

##@ Build

.PHONY: build
build: ## Compile the operator into ./bin.
	@mkdir -p $(BIN)
	$(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN)/$(CMD) ./cmd/$(CMD)

.PHONY: image
image: ## Build the operator container image for the local platform.
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) -t $(K3D_IMAGE) .

.PHONY: clean
clean: ## Remove build artefacts.
	@rm -rf $(BIN)

##@ Test & lint

.PHONY: test
test: ## Run the unit tests with the race detector.
	$(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Rewrite every file with gofmt.
	$(GO) fmt ./...

.PHONY: lint
lint: ## Lint the chart and check the generated files are current.
	helm lint $(CHART)
	helm template $(CHART) > /dev/null
	$(MAKE) generate
	git diff --exit-code -- $(CHART)/files/crds api

.PHONY: lint-go
lint-go: ## Run golangci-lint with the repository's configuration.
	bash hack/install-tools.sh
	$(BIN)/golangci-lint run ./...

# With no BASE the comparison is the merge base with origin/main, which on a branch is the fork
# point and on main itself is HEAD; with no origin at all only the consistency half runs.
.PHONY: check-version
check-version: ## VERSION and Chart.yaml agree, and VERSION went up since BASE.
	bash hack/check-version.sh $${BASE:-$$(git merge-base origin/main HEAD 2>/dev/null || true)}

.PHONY: check
check: check-version vet test lint lint-go ## Everything CI runs.

##@ Code generation

.PHONY: generate
generate: deepcopy manifests ## Run every generator.

.PHONY: deepcopy
deepcopy: ## Regenerate zz_generated.deepcopy.go.
	bash hack/update-deepcopy.sh

.PHONY: manifests
manifests: ## Regenerate the CRD YAMLs into the chart.
	bash hack/update-manifests.sh

##@ k3d

.PHONY: k3d-guard
k3d-guard:
	@if [[ "$(K3D_CLUSTER)" == "hyperfarm-local" || "$(K3D_CLUSTER)" == "local" ]]; then \
		echo "refusing to use the shared cluster '$(K3D_CLUSTER)'; this project owns 'mc-agents'" >&2; \
		exit 1; \
	fi

.PHONY: k3d-up
k3d-up: k3d-guard ## Create the dedicated k3d cluster.
	@if k3d cluster list -o json | grep -q '"name":"$(K3D_CLUSTER)"'; then \
		echo "cluster $(K3D_CLUSTER) already exists"; \
	else \
		k3d cluster create $(K3D_CLUSTER) --agents 1 --wait; \
	fi
	kubectl --context $(K3D_CONTEXT) cluster-info

.PHONY: k3d-down
k3d-down: k3d-guard ## Delete the dedicated k3d cluster.
	k3d cluster delete $(K3D_CLUSTER)

.PHONY: k3d-import
k3d-import: k3d-guard image
	k3d image import $(K3D_IMAGE) -c $(K3D_CLUSTER)

.PHONY: k3d-deploy
k3d-deploy: k3d-import ## Build, import and install the operator into the k3d cluster.
	bash hack/k3d-install.sh $(K3D_CONTEXT) $(NAMESPACE)

.PHONY: k3d-verify
k3d-verify: k3d-guard ## Apply the fixtures in a tenant namespace and assert the operator reconciles them.
	kubectl --context $(K3D_CONTEXT) get namespace $(TENANT) >/dev/null 2>&1 || \
		kubectl --context $(K3D_CONTEXT) create namespace $(TENANT)
	bash hack/verify-k3d.sh $(K3D_CONTEXT) $(TENANT)

# On a cluster with no operator yet: the published chart FROM is installed first, and the point is
# what happens to its objects when the source chart replaces it.
.PHONY: k3d-verify-upgrade
k3d-verify-upgrade: k3d-import ## Install the published release FROM, then upgrade to the source and assert the objects survive.
	kubectl --context $(K3D_CONTEXT) get namespace $(TENANT) >/dev/null 2>&1 || \
		kubectl --context $(K3D_CONTEXT) create namespace $(TENANT)
	bash hack/verify-k3d.sh --from $(FROM) $(K3D_CONTEXT) $(TENANT)

.PHONY: verify
verify: k3d-up k3d-deploy k3d-verify ## Full loop: cluster, install, reconcile check.

.PHONY: verify-upgrade
verify-upgrade: k3d-up k3d-verify-upgrade ## Full loop: cluster, published release FROM, upgrade, survival check.

##@ Help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} \
	      /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 } \
	      /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
