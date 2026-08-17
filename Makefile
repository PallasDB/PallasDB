# PallasDB — single entry point for build, test, lint, proto, and release tasks.
#
# `make help` lists everything. CI calls the same targets a contributor does,
# so a green local `make check` means a green PR.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

MODULE      := github.com/teddymalhan/pallasdb
CMD_PKG     := ./cmd/pallasdb
BINARY      := pallasdb
BIN_DIR     := bin
DIST_DIR    := dist

# Build stamps. `pallasdb version` prints these; cmd/pallasdb declares the
# matching package-scope vars. Keep these three paths identical to the
# `ldflags` block in .goreleaser.yaml.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# The linker names the `main` package `main`, not by its import path, so
# `-X github.com/teddymalhan/pallasdb/cmd/pallasdb.version=...` silently
# no-ops (verified: the binary still printed the default). `main.<var>` is
# the working form. Keep in sync with .goreleaser.yaml and the Dockerfile.
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

GO             ?= go
GOBIN          := $(shell $(GO) env GOPATH)/bin
GOLANGCI_LINT  ?= $(GOBIN)/golangci-lint
GOSEC          ?= $(GOBIN)/gosec
BUF            ?= buf

# Tool versions installed on demand by `make tools`.
GOLANGCI_LINT_VERSION       ?= v2.12.2
GOSEC_VERSION               ?= v2.22.9
PROTOC_GEN_GO_VERSION       ?= v1.36.12
PROTOC_GEN_GO_GRPC_VERSION  ?= v1.6.2

FUZZ_PKG      ?= ./db
FUZZTIME      ?= 60s
COVERPROFILE  ?= coverage.out

DOCKER        ?= docker
DOCKER_IMAGE  ?= ghcr.io/teddymalhan/pallasdb
DOCKER_TAG    ?= $(VERSION)

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nPallasDB targets:\n\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
	@echo

##@ Build

.PHONY: build
build: ## Build the pallasdb binary into bin/
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(CMD_PKG)
	@echo "built $(BIN_DIR)/$(BINARY) ($(VERSION))"

.PHONY: install
install: ## Install pallasdb into $(GOBIN)
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' $(CMD_PKG)

.PHONY: tidy
tidy: ## Run go mod tidy and fail if it changed anything
	$(GO) mod tidy
	git diff --exit-code go.mod go.sum

##@ Test

.PHONY: test
test: ## Run the test suite
	$(GO) test -shuffle=on ./...

.PHONY: test-race
test-race: ## Run the test suite under the race detector with coverage
	$(GO) test -race -shuffle=on -coverprofile=$(COVERPROFILE) ./...

.PHONY: cover
cover: test-race ## Run test-race and open the HTML coverage report
	$(GO) tool cover -html=$(COVERPROFILE)

.PHONY: bench
bench: ## Run Go benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

.PHONY: fuzz
fuzz: ## Fuzz every Fuzz* target in $(FUZZ_PKG) for FUZZTIME each
	@targets="$$($(GO) test -list '^Fuzz' $(FUZZ_PKG) | grep -E '^Fuzz' || true)"; \
	if [ -z "$$targets" ]; then \
		echo "no Fuzz* targets in $(FUZZ_PKG); nothing to do"; \
		exit 0; \
	fi; \
	for t in $$targets; do \
		echo "==> $$t ($(FUZZTIME))"; \
		$(GO) test $(FUZZ_PKG) -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) -fuzzminimizetime 30s; \
	done

##@ Quality

.PHONY: fmt
fmt: ## Format with gofmt and goimports (via golangci-lint)
	$(GOLANGCI_LINT) fmt

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: vet ## Run the blocking linter set (.golangci.yml)
	$(GOLANGCI_LINT) run --timeout 5m

.PHONY: lint-strict
lint-strict: ## Run the advisory linter set (.github/golangci-strict.yml)
	$(GOLANGCI_LINT) run --timeout 10m --config .github/golangci-strict.yml

.PHONY: security
security: ## Run gosec with the reviewed suppressions in .github/gosec.json
	$(GOSEC) -conf .github/gosec.json -exclude-generated -quiet ./...

.PHONY: check
check: lint test-race ## What CI gates on: lint + race tests

##@ Proto

.PHONY: proto
proto: ## Lint protos and regenerate pb/
	$(BUF) lint
	$(BUF) generate

.PHONY: proto-breaking
proto-breaking: ## Check protos for backwards-incompatible changes against main
	$(BUF) breaking --against '.git#branch=main'

##@ Release

.PHONY: docker
docker: ## Build the container image
	$(DOCKER) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(DOCKER_IMAGE):$(DOCKER_TAG) \
		.
	@echo "built $(DOCKER_IMAGE):$(DOCKER_TAG)"

.PHONY: snapshot
snapshot: ## Build a local goreleaser snapshot into dist/ (no publish)
	goreleaser release --snapshot --clean

##@ Misc

.PHONY: tools
tools: ## Install the pinned developer tools into $(GOBIN)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	$(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	$(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	@echo "install buf separately: https://buf.build/docs/installation"

.PHONY: clean
clean: ## Remove build, coverage, and release artifacts
	rm -rf $(BIN_DIR) $(DIST_DIR) $(COVERPROFILE) coverage.html gosec-results.sarif
	$(GO) clean -testcache
