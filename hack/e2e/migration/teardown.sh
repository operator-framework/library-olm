#!/usr/bin/env bash
set -euo pipefail
: "${KIND:?KIND is required}"
"$KIND" delete cluster --name "${E2E_CLUSTER_NAME:-library-olm-e2e}"
