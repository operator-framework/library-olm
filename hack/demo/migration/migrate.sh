#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=hack/demo/migration/common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

usage() {
	echo "Usage: $0 [--dry-run] [--output text|jsonl] [--continue-on-error] (--all|operator [operator ...])" >&2
}

dry_run=false
output=text
continue_on_error=false
targets=()
while [[ $# -gt 0 ]]; do
	case $1 in
	--dry-run) dry_run=true ;;
	--output)
		[[ $# -ge 2 ]] || { usage; exit 2; }
		output=$2
		shift
		;;
	--output=*) output=${1#--output=} ;;
	--continue-on-error) continue_on_error=true ;;
	--all) targets+=(all) ;;
	-*) usage; exit 2 ;;
	*) targets+=("$1") ;;
	esac
	shift
done
if [[ ${#targets[@]} -eq 0 || ( $output != text && $output != jsonl ) ]]; then
	usage
	exit 2
fi
for target in "${targets[@]}"; do
	if [[ $target == all && ${#targets[@]} -ne 1 ]]; then usage; exit 2; fi
done
if [[ ${targets[0]} != all && $continue_on_error == true ]]; then
	echo "--continue-on-error is supported only with --all" >&2
	exit 2
fi

demo_require_cluster
demo_require_binary migrate-operators-v0-to-v1
flags=(--output "$output")
if [[ $dry_run == true ]]; then flags+=(--dry-run); fi

if [[ ${targets[0]} == all ]]; then
	if [[ $continue_on_error == true ]]; then flags+=(--continue-on-error); fi
	"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" convert --all "${flags[@]}"
else
	# Preflight the entire named set before performing the first mutation.
	for target in "${targets[@]}"; do demo_installed_operator "$target"; done
	for target in "${targets[@]}"; do
		operator_fields "$target"
		"$demo_root/bin/migrate-operators-v0-to-v1" --kubeconfig "$KUBECONFIG" convert "$E2E_PACKAGE" -n "$E2E_NAMESPACE" "${flags[@]}"
	done
fi
