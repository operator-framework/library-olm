# Migration-specific build, test, and E2E targets. This file is included by
# the repository Makefile so callers consistently use `make migration/<target>`.

# Output paths for compiled migration CLI binaries.
MIGRATE_OPERATORS_BIN := $(BIN_DIR)/migrate-operators-v0-to-v1
MIGRATE_CATALOGS_BIN  := $(BIN_DIR)/migrate-catalogs-v0-to-v1
COVERAGE_DIR := $(ROOT_DIR)/artifacts/coverage
UNIT_COVERAGE_PROFILE := $(COVERAGE_DIR)/unit.out
UNIT_COVERAGE_REPORT := $(COVERAGE_DIR)/unit.txt
E2E_COVERAGE_PROFILE := $(COVERAGE_DIR)/e2e-cli.out
ALL_COVERAGE_PROFILE := $(COVERAGE_DIR)/all.out

E2E_KUBECONFIG ?= $(ROOT_DIR)/.kubeconfig/library-olm-e2e
E2E_TIMEOUT ?= 30m
E2E_ARTIFACTS ?= $(ROOT_DIR)/artifacts/e2e
E2E_COVERAGE_DIR ?= $(E2E_ARTIFACTS)/coverage
E2E_CLUSTER_NAME ?= library-olm-e2e
# Pin controller releases used by the E2E cluster. Override only to test a
# compatibility candidate; do not use "latest" in CI.
OLM_V0_VERSION ?= v0.46.0
OLM_V1_VERSION ?= v1.11.0
OLM_V0_CRDS ?= https://github.com/operator-framework/operator-lifecycle-manager/releases/download/$(OLM_V0_VERSION)/crds.yaml
OLM_V0_MANIFEST ?= https://github.com/operator-framework/operator-lifecycle-manager/releases/download/$(OLM_V0_VERSION)/olm.yaml
OLM_V1_INSTALL ?= https://github.com/operator-framework/operator-controller/releases/download/$(OLM_V1_VERSION)/install-experimental.sh
OLM_V1_INSTALL_SHA256 ?= 0ce2e6f7ff8244c012fb129b6110c93d1863cd8f2b6bce4a01c94f8bd762b4da
E2E_REAL_OPERATOR_MANIFEST ?= $(ROOT_DIR)/test/e2e/migration/real-operator.yaml
E2E_REAL_OPERATOR_NAMESPACE ?= migration-e2e-real
E2E_REAL_OPERATOR_SUBSCRIPTION ?= ecr-secret-operator
E2E_OPERATOR ?= all

##@ Migration

.PHONY: migration/build
migration/build: migration/build-operators migration/build-catalogs ## Build both migration CLI binaries into bin/

.PHONY: migration/build-operators
migration/build-operators: ## Build migrate-operators-v0-to-v1 into bin/
	@mkdir -p $(BIN_DIR)
	go build -cover -covermode=count -o $(MIGRATE_OPERATORS_BIN) ./migration/examples/cmd/migrate-operators-v0-to-v1

.PHONY: migration/build-catalogs
migration/build-catalogs: ## Build migrate-catalogs-v0-to-v1 into bin/
	@mkdir -p $(BIN_DIR)
	go build -cover -covermode=count -o $(MIGRATE_CATALOGS_BIN) ./migration/examples/cmd/migrate-catalogs-v0-to-v1

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

.PHONY: migration/e2e-setup
migration/e2e-setup: $(KIND) ## Create kind and install pinned OLMv0 and OLMv1 releases
	E2E_KUBECONFIG="$(E2E_KUBECONFIG)" E2E_CLUSTER_NAME="$(E2E_CLUSTER_NAME)" KIND="$(KIND)" OLM_V0_CRDS="$(OLM_V0_CRDS)" OLM_V0_MANIFEST="$(OLM_V0_MANIFEST)" OLM_V1_INSTALL="$(OLM_V1_INSTALL)" OLM_V1_INSTALL_SHA256="$(OLM_V1_INSTALL_SHA256)" ./hack/e2e/migration/setup.sh

.PHONY: migration/e2e-teardown
migration/e2e-teardown: $(KIND) ## Delete the dedicated migration kind cluster
	E2E_CLUSTER_NAME="$(E2E_CLUSTER_NAME)" KIND="$(KIND)" ./hack/e2e/migration/teardown.sh

.PHONY: migration/e2e-install-v0
migration/e2e-install-v0: ## Install one migration E2E operator (E2E_OPERATOR=name) or all via OLMv0
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/install-v0.sh "$$operator"; done

.PHONY: migration/test-e2e-live-matrix
migration/test-e2e-live-matrix: migration/build ## Install and migrate all three operators from live OLMv0
	@set -euo pipefail; while IFS=$$'\t' read -r package channel namespace; do [[ -z "$$package" || "$$package" == \#* ]] && continue; coverage_dir="$(E2E_COVERAGE_DIR)/live/$$package"; artifact_dir="$(E2E_ARTIFACTS)/live/$$package"; mkdir -p "$$coverage_dir"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/install-v0.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" GOCOVERDIR="$$coverage_dir" E2E_ARTIFACTS="$$artifact_dir" E2E_SUITE=real-operator E2E_NAMESPACE="$$namespace" E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; done < test/e2e/migration/operators.tsv

.PHONY: migration/e2e-delete-v1
migration/e2e-delete-v1: ## Delete one migration E2E operator as OLMv1, or all
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$operator"; done

.PHONY: migration/test-e2e-real-operator
migration/test-e2e-real-operator: migration/build ## Run real-operator migration smoke tests against the live kubeconfig
	@mkdir -p "$(E2E_ARTIFACTS)/real-operator"
	@mkdir -p "$(E2E_COVERAGE_DIR)/real-operator"
	KUBECONFIG="$(E2E_KUBECONFIG)" GOCOVERDIR="$(E2E_COVERAGE_DIR)/real-operator" E2E_SUITE=real-operator E2E_MANIFEST="$(E2E_REAL_OPERATOR_MANIFEST)" E2E_NAMESPACE="$(E2E_REAL_OPERATOR_NAMESPACE)" E2E_SUBSCRIPTION="$(E2E_REAL_OPERATOR_SUBSCRIPTION)" E2E_ARTIFACTS="$(E2E_ARTIFACTS)/real-operator" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"

.PHONY: migration/report-coverage-all
migration/report-coverage-all: ## Display coverage from existing unit and collected E2E CLI profiles
	@coverage_dirs="$$(find "$(E2E_COVERAGE_DIR)" -type f -name 'covmeta.*' -printf '%h\n' 2>/dev/null | sort -u | paste -sd, -)"; test -n "$$coverage_dirs" || { echo "no E2E CLI coverage found; run both E2E matrices before migration/test-coverage-all" >&2; exit 2; }; go tool covdata textfmt -i="$$coverage_dirs" -o="$(E2E_COVERAGE_PROFILE)"; awk 'FNR == 1 { next } { key = $$1 " " $$2; if (!(key in count)) order[++n] = key; count[key] += $$3 } END { print "mode: count"; for (i = 1; i <= n; i++) print order[i] " " count[order[i]] }' "$(UNIT_COVERAGE_PROFILE)" "$(E2E_COVERAGE_PROFILE)" > "$(ALL_COVERAGE_PROFILE)"; go tool cover -func="$(ALL_COVERAGE_PROFILE)"

.PHONY: migration/test-coverage-all
migration/test-coverage-all: migration/test-coverage migration/report-coverage-all ## Run unit tests and display combined unit and collected E2E CLI coverage

.PHONY: migration/clean
migration/clean: ## Remove compiled migration CLI binaries from bin/
	rm -f $(MIGRATE_OPERATORS_BIN) $(MIGRATE_CATALOGS_BIN)
