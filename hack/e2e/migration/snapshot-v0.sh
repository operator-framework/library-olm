#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/operators.sh"

operator_fields "${1:?usage: snapshot-v0.sh <operator>}"
snapshot_dir="$root_dir/e2e/migration/fixtures/snapshots/$E2E_PACKAGE"
mkdir -p "$snapshot_dir"
csv=$(kubectl -n "$E2E_NAMESPACE" get "subscription/$E2E_PACKAGE" -o jsonpath='{.status.installedCSV}')
install_plan=$(kubectl -n "$E2E_NAMESPACE" get "subscription/$E2E_PACKAGE" -o jsonpath='{.status.installPlanRef.name}')
# The snapshot intentionally records the OLMv0 inputs and installed resources.
# Fixture replay reapplies these to a fresh test namespace; generated status and
# cluster-scoped resources remain owned by the active OLM controllers. Secrets
# are deliberately excluded: snapshots are committed test fixtures and must not
# capture credentials or generated serving certificates.
kubectl -n "$E2E_NAMESPACE" get \
	operatorgroup,\
	"subscription/$E2E_PACKAGE",\
	"csv/$csv",\
	"installplan/$install_plan" \
	-o yaml >"$snapshot_dir/olmv0.yaml"
kubectl -n "$E2E_NAMESPACE" get all,cm,sa,role,rolebinding -l "olm.owner=$csv" -o yaml >"$snapshot_dir/namespaced-resources.yaml" || true
kubectl get crd -l "operators.coreos.com/$E2E_PACKAGE.$E2E_NAMESPACE" -o yaml >"$snapshot_dir/crds.yaml" || true

# Snapshots are reapplied to fresh clusters. Remove fields assigned by the API
# server while retaining labels, specs, and status captured from the OLMv0
# installation. Status is restored separately by fixture replay.
for snapshot in "$snapshot_dir"/*.yaml; do
	yq -i 'del(.metadata.creationTimestamp, .metadata.generation, .metadata.managedFields, .metadata.resourceVersion, .metadata.uid, .items[].metadata.creationTimestamp, .items[].metadata.generation, .items[].metadata.managedFields, .items[].metadata.resourceVersion, .items[].metadata.uid, .items[].metadata.ownerReferences)' "$snapshot"
done
