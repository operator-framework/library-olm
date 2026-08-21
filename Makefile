SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
BIN_DIR  := $(ROOT_DIR)/bin

GOLANG_VERSION := $(shell sed -En 's/^go (.*)$$/\1/p' "go.mod")

# bingo manages consistent tooling versions.
include .bingo/Variables.mk

# Output paths for compiled CLI binaries
MIGRATE_OPERATORS_BIN := $(BIN_DIR)/migrate-operators-v0-to-v1
MIGRATE_CATALOGS_BIN  := $(BIN_DIR)/migrate-catalogs-v0-to-v1

##@ General

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build

.PHONY: build
build: build-migrate-operators build-migrate-catalogs ## Build both CLI binaries into bin/

.PHONY: build-migrate-operators
build-migrate-operators: ## Build migrate-operators-v0-to-v1 into bin/
	@mkdir -p $(BIN_DIR)
	go build -o $(MIGRATE_OPERATORS_BIN) ./migration/examples/cmd/migrate-operators-v0-to-v1

.PHONY: build-migrate-catalogs
build-migrate-catalogs: ## Build migrate-catalogs-v0-to-v1 into bin/
	@mkdir -p $(BIN_DIR)
	go build -o $(MIGRATE_CATALOGS_BIN) ./migration/examples/cmd/migrate-catalogs-v0-to-v1

.PHONY: build-all
build-all: ## Build and verify all packages (library + CLIs)
	go build ./...

##@ Test

.PHONY: test
test: ## Run unit tests
	go test ./... -count=1

.PHONY: test-verbose
test-verbose: ## Run unit tests with verbose output
	go test ./... -v -count=1

##@ Lint & Verify

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Run gofmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Run go mod tidy
	go mod tidy

.PHONY: verify
verify: tidy fmt vet lint ## Run all verification steps (tidy, fmt, vet, lint)
	@git diff --exit-code || (echo "Files modified by verify — please commit the changes" && exit 1)

.PHONY: api-diff
api-diff: $(GO_APIDIFF) ## Check for breaking API changes against origin/main
	$(GO_APIDIFF) origin/main --repo-path=. --print-compatible

##@ Clean

.PHONY: clean
clean: ## Remove built binaries from bin/
	rm -f $(MIGRATE_OPERATORS_BIN) $(MIGRATE_CATALOGS_BIN)
