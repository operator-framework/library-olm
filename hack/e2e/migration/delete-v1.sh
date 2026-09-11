#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/operators.sh"

operator_fields "${1:?usage: delete-v1.sh <operator>}"
# Delete CE first, then any revisions it owned. This is the explicit OLMv1
# cleanup path; it never deletes shared CRDs or the source CatalogSource.
kubectl delete "clusterextension/$E2E_PACKAGE" --ignore-not-found --wait=true
kubectl delete clusterobjectsets -l "olm.operatorframework.io/owner-name=$E2E_PACKAGE" --ignore-not-found --wait=true
# Ref Secrets live in the OLMv1 system namespace rather than the operator's
# installation namespace and are not garbage-collected by ClusterObjectSet.
# Remove them as part of test cleanup so a repeat migration can reuse its
# deterministic revision and Secret names.
kubectl -n "${E2E_OLMV1_NAMESPACE:-olmv1-system}" delete secret \
	-l "olm.operatorframework.io/owner-name=$E2E_PACKAGE" \
	--ignore-not-found --wait=true
