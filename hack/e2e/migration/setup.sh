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

# Setup is for a fresh installation, not upgrades or repair. Refuse existing
# OLM namespaces or APIs so partial installations also require explicit teardown.
existing=$(kubectl --kubeconfig "$E2E_KUBECONFIG" get \
 namespace/olm namespace/olmv1-system \
 crd/clusterserviceversions.operators.coreos.com \
 crd/clusterextensions.olm.operatorframework.io \
 crd/clusterobjectsets.olm.operatorframework.io \
 crd/clusterobjectdeployments.olm.operatorframework.io \
 --ignore-not-found -o name)
if [[ -n "$existing" ]]; then
 printf 'OLM resources already exist in kind cluster %s. Run E2E_CLUSTER_NAME=%s make migration/e2e-teardown, then rerun setup.\n' "$cluster_name" "$cluster_name" >&2
 exit 1
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
E2E_CLUSTER_NAME="$cluster_name" "$root_dir/hack/e2e/migration/install-olmv1.sh"
kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Established crd/clusterextensions.olm.operatorframework.io crd/clustercatalogs.olm.operatorframework.io crd/clusterobjectsets.olm.operatorframework.io crd/clusterobjectdeployments.olm.operatorframework.io --timeout=5m
kubectl --kubeconfig "$E2E_KUBECONFIG" -n olmv1-system wait --for=condition=Available deployment/operator-controller-controller-manager deployment/catalogd-controller-manager deployment/object-controller-controller-manager --timeout=5m

# Fixture clusters intentionally use only their committed catalog.
if [[ ${E2E_INSTALL_OLMV0:-true} == true ]]; then
	kubectl --kubeconfig "$E2E_KUBECONFIG" apply -f "https://raw.githubusercontent.com/operator-framework/operator-controller/$OLM_V1_COMMIT/manifests/default-catalogs.yaml"
	kubectl --kubeconfig "$E2E_KUBECONFIG" wait --for=condition=Serving clustercatalog/operatorhubio --timeout=5m
fi
