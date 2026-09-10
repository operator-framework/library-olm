#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/operators.sh"

operator_fields "${1:?usage: install-fixture-v0.sh <operator>}"
snapshot_dir="$root_dir/e2e/migration/fixtures/snapshots/$E2E_PACKAGE"
[[ -f "$snapshot_dir/olmv0.yaml" ]] || { echo "missing snapshot: $snapshot_dir/olmv0.yaml; run e2e-snapshot-v0 first" >&2; exit 2; }

# Replay the captured OLMv0 resources in an isolated namespace. A fixture
# cluster has no OLMv0 controllers, so remove the CSV finalizer that only that
# controller can release before replacing a previous fixture installation.
release_csv_finalizers() {
	local csv
	while IFS= read -r csv; do
		[[ -z $csv ]] && continue
		kubectl -n "$E2E_NAMESPACE" patch "$csv" --type=merge \
			--patch '{"metadata":{"finalizers":[]}}'
	done < <(kubectl -n "$E2E_NAMESPACE" get clusterserviceversions -o name 2>/dev/null || true)
}

release_csv_finalizers
kubectl delete namespace "$E2E_NAMESPACE" --ignore-not-found --wait=true
kubectl create namespace "$E2E_NAMESPACE"
preinstall_fixture="$root_dir/e2e/migration/fixtures/preinstall/$E2E_PACKAGE.yaml"
if [[ -f $preinstall_fixture ]]; then
	kubectl -n "$E2E_NAMESPACE" apply --validate=false -f "$preinstall_fixture"
fi
if [[ -f "$snapshot_dir/crds.yaml" ]]; then
	kubectl apply --server-side --force-conflicts --validate=false -f "$snapshot_dir/crds.yaml"
fi
kubectl apply --validate=false -f "$snapshot_dir/olmv0.yaml"
kubectl apply --validate=false -f "$snapshot_dir/namespaced-resources.yaml" 2>/dev/null || true
# CatalogSources are cluster-independent fixture inputs rather than resources
# owned by an individual operator snapshot. OLMv0 is intentionally absent, but
# the migration CLI needs this source to map each Subscription to OLMv1's
# bootstrap ClusterCatalog.
kubectl create namespace olm --dry-run=client -o yaml | kubectl apply --validate=false -f -
kubectl apply --validate=false -f "$root_dir/e2e/migration/fixtures/operatorhubio-catalogsource.yaml"

# No OLMv0 controller is installed in this cluster. Restore the statuses captured
# from the live OLMv0 installation so fixture migration starts from the same
# steady-state contract without relying on a controller to reconcile them.
restore_statuses() {
	local kind resource name status
	for kind in Subscription ClusterServiceVersion InstallPlan OperatorGroup; do
		case $kind in
			Subscription) resource=subscriptions ;;
			ClusterServiceVersion) resource=clusterserviceversions ;;
			InstallPlan) resource=installplans ;;
			OperatorGroup) resource=operatorgroups ;;
		esac
		while IFS= read -r name; do
			[[ -z $name ]] && continue
			status=$(yq -o=json -I=0 ".items[] | select(.kind == \"$kind\" and .metadata.name == \"$name\") | .status" "$snapshot_dir/olmv0.yaml")
			[[ $status == null ]] && continue
			kubectl -n "$E2E_NAMESPACE" patch "$resource/$name" --subresource=status --type=merge --patch "{\"status\":$status}"
		done < <(yq -r ".items[] | select(.kind == \"$kind\" and .status != null) | .metadata.name" "$snapshot_dir/olmv0.yaml")
	done
}
restore_statuses
