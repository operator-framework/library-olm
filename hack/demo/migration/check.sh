#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

usage() { echo "Usage: $0 [--output text|jsonl] [all|operator]" >&2; }

output=text
if [[ ${1:-} == --output ]]; then
	[[ $# -ge 2 ]] || { usage; exit 2; }
	output=$2
	shift 2
elif [[ ${1:-} == --output=* ]]; then
	output=${1#--output=}
	shift
fi
if [[ $# -gt 1 || ( $output != text && $output != jsonl ) ]]; then
	usage
	exit 2
fi

demo_require_cluster
demo_require_binary migrate-operators-v0-to-v1

if [[ $# -eq 0 || $1 == all ]]; then
	"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" check --all --output "$output"
else
	demo_installed_operator "$1"
	"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" check "$E2E_PACKAGE" -n "$E2E_NAMESPACE" --output "$output"
fi
