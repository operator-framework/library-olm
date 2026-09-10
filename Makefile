SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

ROOT_DIR := $(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
BIN_DIR  := $(ROOT_DIR)/bin

GOLANG_VERSION := $(shell sed -En 's/^go (.*)$$/\1/p' "go.mod")

# bingo manages consistent tooling versions.
include .bingo/Variables.mk

# Migration-specific build and test targets live separately so this Makefile
# remains the home for repository-wide targets.
include $(ROOT_DIR)/migration.mk

##@ General

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9\/-]+:.*?##/ { printf "  \033[36m%-36s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Repository Build

.PHONY: build-all
build-all: ## Build and verify all packages (library + CLIs)
	go build ./...

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
clean: migration/clean ## Remove generated repository artifacts
