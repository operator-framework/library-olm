#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/operators.sh"

operator_fields "${1:?usage: install-v0.sh <operator>}"
kubectl delete namespace "$E2E_NAMESPACE" --ignore-not-found --wait=true
kubectl create namespace "$E2E_NAMESPACE" --dry-run=client -o yaml | kubectl apply --validate=false -f -

# Some bundles require user-supplied configuration before their controller pod
# can be created. These source-controlled inputs are also applied by fixture
# replay, keeping both suites faithful to the same installed operator.
preinstall_fixture="$root_dir/e2e/migration/fixtures/preinstall/$E2E_PACKAGE.yaml"
if [[ -f $preinstall_fixture ]]; then
	kubectl -n "$E2E_NAMESPACE" apply --validate=false -f "$preinstall_fixture"
fi

kubectl -n "$E2E_NAMESPACE" apply --validate=false -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: migration-e2e
spec: {}
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: $E2E_PACKAGE
spec:
  channel: $E2E_CHANNEL
  name: $E2E_PACKAGE
  source: operatorhubio-catalog
  sourceNamespace: olm
EOF
kubectl -n "$E2E_NAMESPACE" wait --for=jsonpath='{.status.state}'=AtLatestKnown "subscription/$E2E_PACKAGE" --timeout=15m
csv=$(kubectl -n "$E2E_NAMESPACE" get "subscription/$E2E_PACKAGE" -o jsonpath='{.status.installedCSV}')
kubectl -n "$E2E_NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded "csv/$csv" --timeout=15m
