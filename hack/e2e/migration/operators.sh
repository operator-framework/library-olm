#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
operators_file="$root_dir/e2e/migration/operators.tsv"
: "${KUBECONFIG:?KUBECONFIG must point to the Kind-generated E2E kubeconfig}"

operator_row() {
	awk -F '\t' -v name="$1" '$1 == name { print; exit }' "$operators_file"
}

operator_names() {
	awk -F '\t' 'NF == 3 && $1 !~ /^#/ { print $1 }' "$operators_file"
}

for_each_operator() {
	if [[ ${1:-} == all ]]; then operator_names; else printf '%s\n' "$1"; fi
}

operator_fields() {
	local row
	row=$(operator_row "$1")
	[[ -n "$row" ]] || { echo "unknown E2E operator: $1" >&2; exit 2; }
	IFS=$'\t' read -r E2E_PACKAGE E2E_CHANNEL E2E_NAMESPACE <<<"$row"
	export E2E_PACKAGE E2E_CHANNEL E2E_NAMESPACE
}
