#!/usr/bin/env bash
set -euo pipefail
root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
go -C "$root_dir" tool kind delete cluster --name "${E2E_CLUSTER_NAME:-library-olm-e2e}"
