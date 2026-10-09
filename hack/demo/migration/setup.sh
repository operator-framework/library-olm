#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if [[ $# -ne 0 ]]; then
	echo "Usage: $0" >&2
	exit 2
fi

# The existing E2E bootstrap pins Kind, Kubernetes, OLMv0, and the experimental
# OLMv1 installer. It is idempotent and exports a Kind-generated kubeconfig.
E2E_INSTALL_OLMV0=true make -C "$demo_root" migration/e2e-setup \
	E2E_CLUSTER_NAME="$demo_cluster" E2E_KUBECONFIG="$KUBECONFIG"
demo_require_cluster
make -C "$demo_root" build

# Operator checks need a serving ClusterCatalog. Migrate the OLMv0 bootstrap
# CatalogSource now so install.sh only changes the requested operator.
catalog=operatorhubio-catalog
for attempt in {1..30}; do
	if kubectl -n olm get "catalogsource/$catalog" >/dev/null 2>&1; then
		break
	fi
	if (( attempt == 30 )); then
		echo "CatalogSource olm/$catalog did not appear within five minutes" >&2
		exit 1
	fi
	sleep 10
done
"$demo_root/bin/migrate-catalogs-v0-to-v1" --kubeconfig "$KUBECONFIG"

echo "Demo cluster ready: $demo_cluster"
echo "Kubeconfig: $KUBECONFIG"
echo "Next: hack/demo/migration/install.sh <operator>"
