#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <$(operator_names | paste -sd'|' -)>" >&2
	exit 2
fi

demo_require_cluster
operator_fields "$1"

# install-v0.sh recreates its namespace. Never let a demo install silently
# delete a previous installation or its inspection state.
existing_namespace=$(kubectl get namespace "$E2E_NAMESPACE" --ignore-not-found -o name)
if [[ -n $existing_namespace ]]; then
	echo "Namespace $E2E_NAMESPACE already exists; refusing to replace it" >&2
	exit 1
fi

"$demo_root/hack/e2e/migration/install-v0.sh" "$E2E_PACKAGE"
echo "Installed $E2E_PACKAGE in $E2E_NAMESPACE through OLMv0"
