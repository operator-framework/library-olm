#!/usr/bin/env bash
set -euo pipefail

# Creates a disposable cluster for the migration E2E suites. Manifest locations are
# required inputs: CI supplies immutable, mirrored copies when it cannot access
# the public release URLs.
: "${E2E_KUBECONFIG:?E2E_KUBECONFIG is required}"
: "${OLM_V0_CRDS:?OLM_V0_CRDS is required}"
: "${OLM_V0_MANIFEST:?OLM_V0_MANIFEST is required}"
: "${OLM_V1_INSTALL:?OLM_V1_INSTALL is required}"
: "${OLM_V1_INSTALL_SHA256:?OLM_V1_INSTALL_SHA256 is required}"
: "${KIND:?KIND is required}"

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cluster_name=${E2E_CLUSTER_NAME:-library-olm-e2e}
kind_config=${E2E_KIND_CONFIG:-"$root_dir/e2e/migration/kind-config.yaml"}

mkdir -p "$(dirname "$E2E_KUBECONFIG")"
if "$KIND" get clusters | grep -Fxq "$cluster_name"; then
	# Keep an existing dedicated cluster for iterative fixture development, but
	# always regenerate the kubeconfig from Kind rather than accepting an old or
	# user-supplied file.
	"$KIND" export kubeconfig --name "$cluster_name" --kubeconfig "$E2E_KUBECONFIG"
else
	"$KIND" create cluster --name "$cluster_name" --config "$kind_config" --kubeconfig "$E2E_KUBECONFIG"
fi
kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Ready nodes --all --timeout=3m

# The v0.46 CRD bundle exceeds Kubernetes' client-side last-applied annotation
# limit for the CSV CRD. Server-side apply stores managed fields instead.
kubectl --kubeconfig "$E2E_KUBECONFIG" apply --server-side -f "$OLM_V0_CRDS"
if [[ ${E2E_INSTALL_OLMV0:-true} == true ]]; then
	kubectl --kubeconfig "$E2E_KUBECONFIG" apply -f "$OLM_V0_MANIFEST"
	kubectl --kubeconfig "$E2E_KUBECONFIG" -n olm wait --for=condition=Available deployment/olm-operator --timeout=5m
	kubectl --kubeconfig "$E2E_KUBECONFIG" -n olm wait --for=condition=Available deployment/catalog-operator --timeout=5m
fi

# ClusterObjectSet is an experimental OLMv1 API and is required by the migration
# code, so use operator-controller's experimental installer rather than standard.
# Download a pinned release installer before executing it, restricting redirects
# to HTTPS and verifying its recorded SHA-256.
if ! kubectl --kubeconfig "$E2E_KUBECONFIG" get crd/clusterextensions.olm.operatorframework.io >/dev/null 2>&1; then
	installer=$(mktemp)
	trap 'rm -f "$installer"' EXIT
	curl --fail --location --proto '=https' --proto-redir '=https' --silent --show-error "$OLM_V1_INSTALL" --output "$installer"
	printf '%s  %s\n' "$OLM_V1_INSTALL_SHA256" "$installer" | sha256sum --check --status
	KUBECONFIG="$E2E_KUBECONFIG" bash "$installer"
fi
kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Established crd/clusterextensions.olm.operatorframework.io crd/clustercatalogs.olm.operatorframework.io --timeout=5m
kubectl --kubeconfig "$E2E_KUBECONFIG" -n olmv1-system wait --for=condition=Available deployment/operator-controller-controller-manager deployment/catalogd-controller-manager --timeout=5m
