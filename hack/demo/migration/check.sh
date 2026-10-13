#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

usage() { echo "Usage: $0 [--output text|jsonl] [all|operator]" >&2; }

output=text
targets=()
while [[ $# -gt 0 ]]; do
	case $1 in
	--output)
		[[ $# -ge 2 ]] || { usage; exit 2; }
		output=$2
		shift
		;;
	--output=*) output=${1#--output=} ;;
	-*) usage; exit 2 ;;
	*) targets+=("$1") ;;
	esac
	shift
done
if [[ ${#targets[@]} -gt 1 || ( $output != text && $output != jsonl ) ]]; then
	usage
	exit 2
fi

demo_require_cluster
demo_require_binary migrate-operators-v0-to-v1

if [[ ${#targets[@]} -eq 0 || ${targets[0]} == all ]]; then
	"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" check --all --output "$output"
else
	demo_installed_operator "${targets[0]}"
	"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" check "$E2E_PACKAGE" -n "$E2E_NAMESPACE" --output "$output"
fi
