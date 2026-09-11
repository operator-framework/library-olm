# Migration-specific unit-test targets. This file is included by the repository
# Makefile so callers consistently use `make migration/<target>`.

COVERAGE_DIR := $(ROOT_DIR)/artifacts/coverage
UNIT_COVERAGE_PROFILE := $(COVERAGE_DIR)/unit.out
UNIT_COVERAGE_REPORT := $(COVERAGE_DIR)/unit.txt

##@ Migration

.PHONY: migration/test-unit
migration/test-unit: ## Run migration unit tests
	go test ./migration/... -count=1

.PHONY: migration/test-verbose
migration/test-verbose: ## Run migration unit tests with verbose output
	go test ./migration/... -v -count=1

.PHONY: migration/test-coverage
migration/test-coverage: ## Run migration unit tests and display coverage
	@mkdir -p $(COVERAGE_DIR)
	go test ./migration/... -count=1 -covermode=count -coverprofile=$(UNIT_COVERAGE_PROFILE)
	go tool cover -func=$(UNIT_COVERAGE_PROFILE) | tee $(UNIT_COVERAGE_REPORT)
