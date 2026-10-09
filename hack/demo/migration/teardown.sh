#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

if [[ $# -ne 0 ]]; then
	echo "Usage: $0" >&2
	exit 2
fi

# Only the fixed demo cluster is deleted; the generated kubeconfig remains as
# a record of the context but cannot reach a cluster after deletion.
make -C "$demo_root" migration/e2e-teardown E2E_CLUSTER_NAME="$demo_cluster"
echo "Deleted Kind cluster $demo_cluster (kubeconfig retained at $KUBECONFIG)"
