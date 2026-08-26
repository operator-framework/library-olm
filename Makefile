SHELL := /usr/bin/env bash -o pipefail
.SHELLFLAGS := -ec

# NOTE: This Makefile contains stub targets. Go packages and real build/lint
# targets will be added in a subsequent PR. All targets pass so that CI is
# green while the repository is being bootstrapped.

##@ General

.PHONY: help
help: ## Display this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-28s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Build

.PHONY: build build-all
build build-all: ## Build CLIs (stub: no packages yet)
	@echo "Nothing to build — Go packages will be added in a subsequent PR."

##@ Test

.PHONY: test test-verbose
test test-verbose: ## Run unit tests (stub: no packages yet)
	@echo "Nothing to test — Go packages will be added in a subsequent PR."

##@ Lint & Verify

.PHONY: lint fmt vet tidy verify api-diff
fmt vet tidy: ## No-op stubs until Go packages are present
	@echo "Nothing to format/vet/tidy — Go packages will be added in a subsequent PR."

lint: ## No-op stub until Go packages are present
	@echo "Nothing to lint — Go packages will be added in a subsequent PR."

verify: fmt vet tidy lint ## Run all verification steps (stub)
	@echo "Nothing to verify — Go packages will be added in a subsequent PR."

api-diff: ## Check for breaking API changes (stub: no packages yet)
	@echo "Nothing to diff — Go packages will be added in a subsequent PR."

##@ Clean

.PHONY: clean
clean: ## Remove built binaries from bin/
	@rm -rf bin/
