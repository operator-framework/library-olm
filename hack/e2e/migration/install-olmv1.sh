#!/usr/bin/env bash
set -euo pipefail

# Build the pinned controller: released installers use older experimental APIs.
: "${OLM_V1_COMMIT:?OLM_V1_COMMIT is required}"
: "${E2E_KUBECONFIG:?E2E_KUBECONFIG is required}"
: "${E2E_CLUSTER_NAME:?E2E_CLUSTER_NAME is required}"
: "${KIND:?KIND is required}"
[[ "$OLM_V1_COMMIT" =~ ^[0-9a-f]{40}$ ]] || { echo 'OLM_V1_COMMIT must be a full commit hash' >&2; exit 1; }

source_dir=$(mktemp -d)
trap 'rm -rf "$source_dir"' EXIT
git -C "$source_dir" init --quiet
git -C "$source_dir" fetch --quiet --depth=1 https://github.com/operator-framework/operator-controller.git "$OLM_V1_COMMIT"
git -C "$source_dir" checkout --quiet --detach FETCH_HEAD

# Reuse upstream image names, build targets, manifest generation, and installer.
# Loading into the existing cluster avoids upstream's destructive cluster target.
# setup.sh installs the live catalog after controller installation.
export KUBECONFIG
KUBECONFIG=$(realpath "$E2E_KUBECONFIG")
# The generated Helm manifest uses these image names; do not inherit caller
# image overrides (including make command-line variables) into the build.
make -C "$source_dir" -f Makefile -f - migration-install \
	IMAGE_REGISTRY=quay.io/operator-framework IMAGE_TAG=devel \
	OPCON_IMAGE_REPO=quay.io/operator-framework/operator-controller \
	CATD_IMAGE_REPO=quay.io/operator-framework/catalogd \
	OBJECT_CONTROLLER_IMAGE_REPO=quay.io/operator-framework/object-controller \
	OPCON_IMG=quay.io/operator-framework/operator-controller:devel \
	CATD_IMG=quay.io/operator-framework/catalogd:devel \
	OBJECT_CONTROLLER_IMG=quay.io/operator-framework/object-controller:devel \
	KIND="$KIND" MIGRATION_CLUSTER="$E2E_CLUSTER_NAME" <<'MAKE'
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.PHONY: migration-install
migration-install: docker-build manifests
	$(KIND) load docker-image $(OPCON_IMG) $(CATD_IMG) $(OBJECT_CONTROLLER_IMG) --name $(MIGRATION_CLUSTER)
	MANIFEST=$(EXPERIMENTAL_MANIFEST) DEFAULT_CATALOG=$(CATALOGS_MANIFEST) INSTALL_DEFAULT_CATALOGS=false envsubst '$$DEFAULT_CATALOG,$$CERT_MGR_VERSION,$$INSTALL_DEFAULT_CATALOGS,$$MANIFEST' < scripts/install.tpl.sh | bash
MAKE
