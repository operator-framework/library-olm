#!/usr/bin/env bash
set -euo pipefail

: "${E2E_CLUSTER_NAME:?E2E_CLUSTER_NAME is required}"
: "${E2E_KUBECONFIG:?E2E_KUBECONFIG is required}"
: "${KIND:?KIND is required}"

if [[ ! -f $E2E_KUBECONFIG ]]; then
	echo "Refusing E2E cleanup: kubeconfig $E2E_KUBECONFIG does not exist" >&2
	exit 1
fi

expected_context="kind-$E2E_CLUSTER_NAME"
actual_context=$(kubectl --kubeconfig "$E2E_KUBECONFIG" config current-context)
if [[ $actual_context != "$expected_context" ]]; then
	echo "Refusing E2E cleanup: context $actual_context is not $expected_context" >&2
	exit 1
fi

# Match both the API endpoint and CA to Kind's current cluster, not just a
# user-editable context name in the supplied kubeconfig.
kind_kubeconfig=$(mktemp)
trap 'rm -f "$kind_kubeconfig"' EXIT
if ! "$KIND" get kubeconfig --name "$E2E_CLUSTER_NAME" >"$kind_kubeconfig"; then
	echo "Refusing E2E cleanup: Kind cluster $E2E_CLUSTER_NAME does not exist" >&2
	exit 1
fi
cluster_identity() {
	kubectl --kubeconfig "$1" config view --raw --flatten --minify \
		-o jsonpath='{.clusters[0].cluster.server}{"|"}{.clusters[0].cluster.certificate-authority-data}'
}
actual_identity=$(cluster_identity "$E2E_KUBECONFIG")
expected_identity=$(cluster_identity "$kind_kubeconfig")
if [[ -z $actual_identity || $actual_identity != "$expected_identity" ]]; then
	echo "Refusing E2E cleanup: kubeconfig does not identify Kind cluster $E2E_CLUSTER_NAME" >&2
	exit 1
fi

kubectl --kubeconfig "$E2E_KUBECONFIG" get namespace kube-system >/dev/null
