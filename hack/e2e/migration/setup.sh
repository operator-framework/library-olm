#!/usr/bin/env bash
set -euo pipefail

# Creates a disposable cluster for the migration E2E suites using pinned OLMv0
# manifests and the current experimental operator-controller source.
: "${E2E_KUBECONFIG:?E2E_KUBECONFIG is required}"
: "${OLM_V0_CRDS:?OLM_V0_CRDS is required}"
: "${OLM_V0_MANIFEST:?OLM_V0_MANIFEST is required}"
: "${OLM_V1_COMMIT:?OLM_V1_COMMIT is required}"
: "${KIND:?KIND is required}"

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cluster_name=${E2E_CLUSTER_NAME:-library-olm-e2e}
kind_config=${E2E_KIND_CONFIG:-"$root_dir/test/e2e/migration/kind-config.yaml"}

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

# A reused cluster keeps its installed controller. Reject incompatible schemas
# before making further setup changes; only NotFound permits installation.
existing_controller=$(kubectl --kubeconfig "$E2E_KUBECONFIG" get crd/clusterextensions.olm.operatorframework.io --ignore-not-found -o name)
validate_revision_crds() {
	kubectl --kubeconfig "$E2E_KUBECONFIG" get crd/clusterobjectsets.olm.operatorframework.io crd/clusterobjectdeployments.olm.operatorframework.io -o json |
		python3 "$root_dir/hack/e2e/migration/validate-revision-crds.py"
}
if [[ -n "$existing_controller" ]]; then
	validate_revision_crds
fi

# The v0.46 CRD bundle exceeds Kubernetes' client-side last-applied annotation
# limit for the CSV CRD. Server-side apply stores managed fields instead.
kubectl --kubeconfig "$E2E_KUBECONFIG" apply --server-side -f "$OLM_V0_CRDS"
if [[ ${E2E_INSTALL_OLMV0:-true} == true ]]; then
	kubectl --kubeconfig "$E2E_KUBECONFIG" apply -f "$OLM_V0_MANIFEST"
	kubectl --kubeconfig "$E2E_KUBECONFIG" -n olm wait --for=condition=Available deployment/olm-operator --timeout=5m
	kubectl --kubeconfig "$E2E_KUBECONFIG" -n olm wait --for=condition=Available deployment/catalog-operator --timeout=5m
fi

# Install the controller matching the current experimental COS/COD definitions.
if [[ -z "$existing_controller" ]]; then
	E2E_CLUSTER_NAME="$cluster_name" "$root_dir/hack/e2e/migration/install-olmv1.sh"
fi
kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Established crd/clusterextensions.olm.operatorframework.io crd/clustercatalogs.olm.operatorframework.io --timeout=5m
kubectl --kubeconfig "$E2E_KUBECONFIG" -n olmv1-system wait --for=condition=Available deployment/operator-controller-controller-manager deployment/catalogd-controller-manager deployment/object-controller-controller-manager --timeout=5m

validate_revision_crds
# Install/repair this catalog even when an earlier setup already installed the
# controller. Fixture clusters intentionally use only their committed catalog.
if [[ ${E2E_INSTALL_OLMV0:-true} == true ]]; then
	kubectl --kubeconfig "$E2E_KUBECONFIG" apply -f "https://raw.githubusercontent.com/operator-framework/operator-controller/$OLM_V1_COMMIT/manifests/default-catalogs.yaml"
	kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Serving clustercatalog/operatorhubio --timeout=5m
fi
