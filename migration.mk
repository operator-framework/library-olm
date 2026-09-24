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
E2E_FIXTURE_CLUSTER_NAME ?= library-olm-fixture-e2e
E2E_FIXTURE_KUBECONFIG ?= $(ROOT_DIR)/.kubeconfig/library-olm-fixture-e2e
# Pin controller releases used by the E2E cluster. Override only to test a
# compatibility candidate; do not use "latest" in CI.
OLM_V0_VERSION ?= v0.46.0
OLM_V1_VERSION ?= v1.12.0
OLM_V0_CRDS ?= https://github.com/operator-framework/operator-lifecycle-manager/releases/download/$(OLM_V0_VERSION)/crds.yaml
OLM_V0_MANIFEST ?= https://github.com/operator-framework/operator-lifecycle-manager/releases/download/$(OLM_V0_VERSION)/olm.yaml
OLM_V1_INSTALL ?= https://github.com/operator-framework/operator-controller/releases/download/$(OLM_V1_VERSION)/install-experimental.sh
OLM_V1_INSTALL_SHA256 ?= 5c3c86e6d54b21ae7b0b566145ef703f8230e3f4cf4326541d42015a2a195970
E2E_REAL_OPERATOR_MANIFEST ?= $(ROOT_DIR)/test/e2e/migration/real-operator.yaml
E2E_REAL_OPERATOR_NAMESPACE ?= migration-e2e-real
E2E_REAL_OPERATOR_SUBSCRIPTION ?= ecr-secret-operator
E2E_OPERATOR ?= all
E2E_MIGRATION_IMAGE ?= library-olm-migration-e2e:dev
# Retain migration resources after a focused E2E run for manual inspection.
E2E_KEEP_RESOURCES ?= false

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

.PHONY: migration/build-e2e-image
migration/build-e2e-image: $(KIND) ## Build and load an uninstrumented migration CLI image into the fixture kind cluster
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -o $(MIGRATE_OPERATORS_BIN) ./migration/examples/cmd/migrate-operators-v0-to-v1
	CGO_ENABLED=0 go build -o $(MIGRATE_CATALOGS_BIN) ./migration/examples/cmd/migrate-catalogs-v0-to-v1
	docker build --tag "$(E2E_MIGRATION_IMAGE)" --file test/e2e/migration/migration-tool.Dockerfile .
	$(KIND) load docker-image --name "$(E2E_FIXTURE_CLUSTER_NAME)" "$(E2E_MIGRATION_IMAGE)"

.PHONY: migration/test-unit
migration/test-unit: ## Run migration unit tests and write a coverage profile
	@mkdir -p $(COVERAGE_DIR)
	go test ./migration/... -count=1 -covermode=count -coverprofile=$(UNIT_COVERAGE_PROFILE)
	go tool cover -func=$(UNIT_COVERAGE_PROFILE) | tee $(UNIT_COVERAGE_REPORT)

.PHONY: migration/test-verbose
migration/test-verbose: ## Run migration unit tests with verbose output
	go test ./migration/... -v -count=1

.PHONY: migration/e2e-setup
migration/e2e-setup: $(KIND) ## Create kind and install pinned OLMv0 and OLMv1 releases
	E2E_KUBECONFIG="$(E2E_KUBECONFIG)" E2E_CLUSTER_NAME="$(E2E_CLUSTER_NAME)" KIND="$(KIND)" OLM_V0_CRDS="$(OLM_V0_CRDS)" OLM_V0_MANIFEST="$(OLM_V0_MANIFEST)" OLM_V1_INSTALL="$(OLM_V1_INSTALL)" OLM_V1_INSTALL_SHA256="$(OLM_V1_INSTALL_SHA256)" ./hack/e2e/migration/setup.sh

.PHONY: migration/e2e-fixture-setup
migration/e2e-fixture-setup: $(KIND) $(CRANE) ## Create fixture kind cluster with OLMv0 APIs but no OLMv0 controllers
	E2E_KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" E2E_CLUSTER_NAME="$(E2E_FIXTURE_CLUSTER_NAME)" E2E_INSTALL_OLMV0=false KIND="$(KIND)" OLM_V0_CRDS="$(OLM_V0_CRDS)" OLM_V0_MANIFEST="$(OLM_V0_MANIFEST)" OLM_V1_INSTALL="$(OLM_V1_INSTALL)" OLM_V1_INSTALL_SHA256="$(OLM_V1_INSTALL_SHA256)" ./hack/e2e/migration/setup.sh
	KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" CRANE="$(CRANE)" ./hack/e2e/migration/build-fixture-catalog.sh

.PHONY: migration/e2e-teardown
migration/e2e-teardown: $(KIND) ## Delete the dedicated migration kind cluster
	E2E_CLUSTER_NAME="$(E2E_CLUSTER_NAME)" KIND="$(KIND)" ./hack/e2e/migration/teardown.sh

.PHONY: migration/e2e-install-v0
migration/e2e-install-v0: ## Install one migration E2E operator (E2E_OPERATOR=name) or all via OLMv0
	@if [[ "$(E2E_OPERATOR)" != all ]]; then E2E_OPERATOR="$(E2E_OPERATOR)" KUBECONFIG="$(E2E_KUBECONFIG)" bash -c 'source "$$1"; operator_fields "$$E2E_OPERATOR"' -- ./hack/e2e/migration/operators.sh; fi
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/install-v0.sh "$$operator"; done

.PHONY: migration/e2e-snapshot-v0
migration/e2e-snapshot-v0: ## Snapshot OLMv0 installs for one migration E2E operator or all
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/snapshot-v0.sh "$$operator"; done

.PHONY: migration/e2e-install-fixture-v0
migration/e2e-install-fixture-v0: ## Replay one OLMv0 install snapshot, or all, as fixtures
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/install-fixture-v0.sh "$$operator"; done

.PHONY: migration/test-e2e-live-matrix
migration/test-e2e-live-matrix: migration/build ## Install and migrate all three operators from live OLMv0
	@set -euo pipefail; while IFS=$$'\t' read -r package channel namespace; do [[ -z "$$package" || "$$package" == \#* ]] && continue; coverage_dir="$(E2E_COVERAGE_DIR)/live/$$package"; artifact_dir="$(E2E_ARTIFACTS)/live/$$package"; mkdir -p "$$coverage_dir"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/install-v0.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" GOCOVERDIR="$$coverage_dir" E2E_ARTIFACTS="$$artifact_dir" E2E_SUITE=real-operator E2E_NAMESPACE="$$namespace" E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; done < test/e2e/migration/operators.tsv

.PHONY: migration/test-e2e-live-namespace-delete
migration/test-e2e-live-namespace-delete: migration/build ## Verify acknowledged source-namespace deletion with live OLMv0
	@set -euo pipefail; package=ecr-secret-operator; namespace=migration-e2e-ecr-secret; target="$$namespace-target"; coverage_dir="$(E2E_COVERAGE_DIR)/live/cross-namespace-delete"; artifact_dir="$(E2E_ARTIFACTS)/live/cross-namespace-delete"; mkdir -p "$$coverage_dir"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" kubectl delete namespace "$$target" --ignore-not-found --wait=true; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/install-v0.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" GOCOVERDIR="$$coverage_dir" E2E_ARTIFACTS="$$artifact_dir" E2E_SUITE=real-operator E2E_LIVE_NAMESPACE_DELETE_TEST=true E2E_NAMESPACE="$$namespace" E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e ./test/e2e/migration -run '^TestLiveCrossNamespaceDeletionMigration$$' -timeout "$(E2E_TIMEOUT)"; KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_KUBECONFIG)" kubectl delete namespace "$$target" --ignore-not-found --wait=true

.PHONY: migration/test-e2e-fixture-matrix
migration/test-e2e-fixture-matrix: migration/build ## Replay and migrate all three OLMv0 install snapshots
	@set -euo pipefail; while IFS=$$'\t' read -r package channel namespace; do [[ -z "$$package" || "$$package" == \#* ]] && continue; coverage_dir="$(E2E_COVERAGE_DIR)/fixture/$$package"; artifact_dir="$(E2E_ARTIFACTS)/fixture/$$package"; mkdir -p "$$coverage_dir"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/install-fixture-v0.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" GOCOVERDIR="$$coverage_dir" E2E_ARTIFACTS="$$artifact_dir" E2E_SUITE=fixture E2E_NAMESPACE="$$namespace" E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; done < test/e2e/migration/operators.tsv

.PHONY: migration/test-e2e-cross-namespace
migration/test-e2e-cross-namespace: migration/build ## Verify fixture migration into a different install namespace
	@set -euo pipefail; package=ecr-secret-operator; namespace=migration-e2e-ecr-secret; target="$$namespace-target"; coverage_dir="$(E2E_COVERAGE_DIR)/fixture/cross-namespace"; artifact_dir="$(E2E_ARTIFACTS)/fixture/cross-namespace"; mkdir -p "$$coverage_dir"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" kubectl delete namespace "$$target" --ignore-not-found --wait=true; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/install-fixture-v0.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" GOCOVERDIR="$$coverage_dir" E2E_ARTIFACTS="$$artifact_dir" E2E_SUITE=fixture E2E_CROSS_NAMESPACE_TEST=true E2E_NAMESPACE="$$namespace" E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e ./test/e2e/migration -run '^TestCrossNamespaceMigration$$' -timeout "$(E2E_TIMEOUT)"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" kubectl delete namespace "$$target" --ignore-not-found --wait=true; : 'The source fixture namespace contains captured OLMv0 finalizers. install-fixture-v0.sh releases them before the next replay; CI tears down the Kind cluster.'

.PHONY: migration/test-e2e-in-cluster-job
migration/test-e2e-in-cluster-job: migration/e2e-fixture-setup migration/build-e2e-image ## Run migration CLIs in a fixture-cluster Job (no coverage collection)
	E2E_OPERATOR=ecr-secret-operator E2E_KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" $(MAKE) migration/e2e-delete-v1
	E2E_OPERATOR=ecr-secret-operator $(MAKE) migration/e2e-install-fixture-v0
	KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" E2E_SUITE=in-cluster-job E2E_NAMESPACE=migration-e2e-ecr-secret E2E_SUBSCRIPTION=ecr-secret-operator E2E_MIGRATION_IMAGE="$(E2E_MIGRATION_IMAGE)" E2E_ARTIFACTS="$(E2E_ARTIFACTS)/in-cluster-job" go test -count=1 -tags=e2e ./test/e2e/migration -run '^TestMigrationInClusterJob$$' -timeout "$(E2E_TIMEOUT)"

.PHONY: migration/test-e2e-cos-supersession
migration/test-e2e-cos-supersession: migration/e2e-fixture-setup migration/build ## Verify migration COS is superseded by the catalog revision
	@set -euo pipefail; package=ecr-secret-operator; coverage_profile="$(E2E_COVERAGE_DIR)/cos-supersession/cos-supersession-coverage.out"; mkdir -p "$$(dirname "$$coverage_profile")"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/install-fixture-v0.sh "$$package"; KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" E2E_ARTIFACTS="$(E2E_ARTIFACTS)/cos-supersession" E2E_SUITE=fixture E2E_COS_SUPERSESSION_TEST=true E2E_NAMESPACE=migration-e2e-ecr-secret E2E_SUBSCRIPTION="$$package" go test -count=1 -tags=e2e -covermode=count -coverpkg=./migration/... -coverprofile="$$coverage_profile" ./test/e2e/migration -run '^TestPrecreatedClusterObjectSetSupersession$$' -timeout "$(E2E_TIMEOUT)"; if [[ "$(E2E_KEEP_RESOURCES)" != true ]]; then KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$package"; else echo "Retaining migration resources for inspection"; fi

.PHONY: migration/e2e-delete-v1
migration/e2e-delete-v1: ## Delete one migration E2E operator as OLMv1, or all
	@if [[ "$(E2E_OPERATOR)" != all ]]; then E2E_OPERATOR="$(E2E_OPERATOR)" KUBECONFIG="$(E2E_KUBECONFIG)" bash -c 'source "$$1"; operator_fields "$$E2E_OPERATOR"' -- ./hack/e2e/migration/operators.sh; fi
	@for operator in $$(awk -F '\t' -v wanted="$(E2E_OPERATOR)" 'NF==3 && $$1 !~ /^#/ && (wanted=="all" || $$1==wanted) {print $$1}' test/e2e/migration/operators.tsv); do KUBECONFIG="$(E2E_KUBECONFIG)" ./hack/e2e/migration/delete-v1.sh "$$operator"; done

.PHONY: migration/test-e2e-fixture
migration/test-e2e-fixture: migration/build ## Run deterministic fixture migration tests against the fixture kubeconfig
	@test -n "$(E2E_FIXTURE_MANIFEST)" || (echo "E2E_FIXTURE_MANIFEST must install the fixture CatalogSource and Subscription" >&2; exit 2)
	@test -n "$(E2E_FIXTURE_NAMESPACE)" || (echo "E2E_FIXTURE_NAMESPACE is required" >&2; exit 2)
	@test -n "$(E2E_FIXTURE_SUBSCRIPTION)" || (echo "E2E_FIXTURE_SUBSCRIPTION is required" >&2; exit 2)
	@mkdir -p "$(E2E_ARTIFACTS)/fixture"
	@mkdir -p "$(E2E_COVERAGE_DIR)/fixture"
	KUBECONFIG="$(E2E_FIXTURE_KUBECONFIG)" GOCOVERDIR="$(E2E_COVERAGE_DIR)/fixture" E2E_SUITE=fixture E2E_MANIFEST="$(E2E_FIXTURE_MANIFEST)" E2E_NAMESPACE="$(E2E_FIXTURE_NAMESPACE)" E2E_SUBSCRIPTION="$(E2E_FIXTURE_SUBSCRIPTION)" E2E_ARTIFACTS="$(E2E_ARTIFACTS)/fixture" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"

.PHONY: migration/test-e2e-real-operator
migration/test-e2e-real-operator: migration/build ## Run real-operator migration smoke tests against the live kubeconfig
	@mkdir -p "$(E2E_ARTIFACTS)/real-operator"
	@mkdir -p "$(E2E_COVERAGE_DIR)/real-operator"
	KUBECONFIG="$(E2E_KUBECONFIG)" GOCOVERDIR="$(E2E_COVERAGE_DIR)/real-operator" E2E_SUITE=real-operator E2E_MANIFEST="$(E2E_REAL_OPERATOR_MANIFEST)" E2E_NAMESPACE="$(E2E_REAL_OPERATOR_NAMESPACE)" E2E_SUBSCRIPTION="$(E2E_REAL_OPERATOR_SUBSCRIPTION)" E2E_ARTIFACTS="$(E2E_ARTIFACTS)/real-operator" go test -count=1 -tags=e2e ./test/e2e/migration -timeout "$(E2E_TIMEOUT)"

.PHONY: migration/report-coverage-all
migration/report-coverage-all: ## Display coverage from existing unit, E2E CLI, and E2E package profiles
	@mkdir -p $(COVERAGE_DIR)
	@test -f "$(UNIT_COVERAGE_PROFILE)" || { echo "unit coverage is missing; run make migration/test-unit first" >&2; exit 2; }
	@coverage_dirs="$$(find "$(E2E_COVERAGE_DIR)" -type f -name 'covmeta.*' -printf '%h\n' 2>/dev/null | sort -u | paste -sd, -)"; test -n "$$coverage_dirs" || { echo "no E2E CLI coverage found; run both E2E matrices before migration/report-coverage-all" >&2; exit 2; }; go tool covdata textfmt -i="$$coverage_dirs" -o="$(E2E_COVERAGE_PROFILE)"; profiles=("$(UNIT_COVERAGE_PROFILE)" "$(E2E_COVERAGE_PROFILE)"); while IFS= read -r profile; do profiles+=("$$profile"); done < <(find "$(E2E_COVERAGE_DIR)" -type f -name '*-coverage.out' -print); awk 'FNR == 1 { next } { key = $$1 " " $$2; if (!(key in count)) order[++n] = key; count[key] += $$3 } END { print "mode: count"; for (i = 1; i <= n; i++) print order[i] " " count[order[i]] }' "$${profiles[@]}" > "$(ALL_COVERAGE_PROFILE)"; go tool cover -func="$(ALL_COVERAGE_PROFILE)"

.PHONY: migration/test-coverage-all
migration/test-coverage-all: migration/test-unit migration/report-coverage-all ## Run unit tests and display combined unit and collected E2E CLI coverage

.PHONY: migration/clean
migration/clean: ## Remove compiled migration CLI binaries from bin/
	rm -f $(MIGRATE_OPERATORS_BIN) $(MIGRATE_CATALOGS_BIN)
