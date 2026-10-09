#!/usr/bin/env bash
# Shared, dedicated-cluster inputs for the migration demonstration scripts.
set -euo pipefail

demo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
demo_cluster=library-olm-demo
export KUBECONFIG="$demo_root/.kubeconfig/$demo_cluster"

# Use the same package/channel/namespace list as the live E2E suite.
# shellcheck source=hack/e2e/migration/operators.sh
source "$demo_root/hack/e2e/migration/operators.sh"

demo_require_cluster() {
	if [[ ! -f $KUBECONFIG ]]; then
		echo "Demo kubeconfig is missing: $KUBECONFIG; run setup.sh first" >&2
		return 1
	fi
	local context
	context=$(kubectl config current-context)
	if [[ $context != "kind-$demo_cluster" ]]; then
		echo "Refusing to use context $context; expected kind-$demo_cluster" >&2
		return 1
	fi
	kubectl get nodes >/dev/null
}

demo_require_binary() {
	if [[ ! -x $demo_root/bin/$1 ]]; then
		echo "Missing bin/$1; run setup.sh (or make build) first" >&2
		return 1
	fi
}

demo_installed_operator() {
	operator_fields "$1"
	if ! kubectl -n "$E2E_NAMESPACE" get "subscription/$E2E_PACKAGE" >/dev/null 2>&1; then
		echo "$E2E_PACKAGE is not installed as an OLMv0 Subscription in $E2E_NAMESPACE" >&2
		return 1
	fi
}
