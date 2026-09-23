#!/usr/bin/env bash
set -euo pipefail

# Build a self-contained FBC and bundle images from committed OLMv0 snapshots.
# The registry is inside the fixture cluster, so catalogd never pulls fixture
# catalog metadata or bundle content from Quay.
: "${KUBECONFIG:?KUBECONFIG is required}"
: "${CRANE:?CRANE is required}"

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
registry_namespace=migration-e2e-registry
registry_service=fixture-registry
registry_host="$registry_service.$registry_namespace.svc.cluster.local:5000"
# Crane treats localhost registry references as HTTP. 127.0.0.2 is another
# loopback address, but is treated as an HTTPS registry. It is included as an
# IP SAN on the fixture registry certificate.
push_host=127.0.0.2:5001
work_dir=$(mktemp -d)
port_forward_pid=
cleanup() {
	[[ -n ${port_forward_pid:-} ]] && kill "$port_forward_pid" 2>/dev/null || true
	rm -rf "$work_dir"
}
trap cleanup EXIT

# Fixture setup is idempotent. Remove a prior CatalogSource migration result
# before replaying the committed CatalogSource with the locally built image.
# The experimental operator-controller installer creates an `operatorhubio`
# ClusterCatalog that resolves packages from Quay. Fixture tests must never
# select mutable external catalog content instead of their committed snapshots.
kubectl delete clustercatalog/operatorhubio --ignore-not-found --wait=true
kubectl delete clustercatalog/operatorhubio-catalog --ignore-not-found --wait=true
kubectl apply -f "$root_dir/test/e2e/migration/fixtures/registry.yaml"
kubectl -n "$registry_namespace" wait --for=condition=Ready certificate/fixture-registry-tls --timeout=3m
kubectl -n "$registry_namespace" wait --for=condition=Available deployment/$registry_service --timeout=3m
registry_ca="$work_dir/olmv1-ca.crt"
kubectl -n cert-manager get secret/olmv1-ca -o jsonpath='{.data.ca\.crt}' | base64 --decode >"$registry_ca"
[[ -s $registry_ca ]] || { echo "OLMv1 CA certificate is empty" >&2; exit 1; }
kubectl -n "$registry_namespace" port-forward --address 127.0.0.2 deployment/$registry_service 5001:5000 >"$work_dir/port-forward.log" 2>&1 &
port_forward_pid=$!
readiness_deadline=$((SECONDS + 60))
until curl --cacert "$registry_ca" --fail --silent "https://$push_host/v2/" >/dev/null; do
	if ! kill -0 "$port_forward_pid" 2>/dev/null; then
		echo "fixture registry port-forward exited unexpectedly:" >&2
		cat "$work_dir/port-forward.log" >&2
		exit 1
	fi
	if ((SECONDS >= readiness_deadline)); then
		echo "timed out waiting for fixture registry port-forward:" >&2
		cat "$work_dir/port-forward.log" >&2
		exit 1
	fi
	sleep 1
done

catalog_dir="$work_dir/catalog"
mkdir -p "$catalog_dir/configs"
for package in ecr-secret-operator external-secrets-operator redis-operator; do
	snapshot_dir="$root_dir/test/e2e/migration/fixtures/snapshots/$package"
	channel=$(awk -F '\t' -v package="$package" '$1 == package { print $2 }' "$root_dir/test/e2e/migration/operators.tsv")
	[[ -n $channel ]] || { echo "no channel configured for $package" >&2; exit 1; }
	csv_name=$(yq -r '.items[] | select(.kind == "ClusterServiceVersion") | .metadata.name' "$snapshot_dir/olmv0.yaml")
	version=$(yq -r '.items[] | select(.kind == "ClusterServiceVersion") | .spec.version' "$snapshot_dir/olmv0.yaml")
	properties=$(yq -r '.items[] | select(.kind == "ClusterServiceVersion") | .metadata.annotations."operatorframework.io/properties"' "$snapshot_dir/olmv0.yaml" | jq -c '.properties')
	bundle_dir="$work_dir/$package"
	mkdir -p "$bundle_dir/manifests" "$bundle_dir/metadata"
	yq -o=json '.items[] | select(.kind == "ClusterServiceVersion")' "$snapshot_dir/olmv0.yaml" >"$bundle_dir/manifests/$csv_name.json"
	cp "$snapshot_dir/crds.yaml" "$bundle_dir/manifests/crds.yaml"
	cat >"$bundle_dir/metadata/annotations.yaml" <<EOF
annotations:
  operators.operatorframework.io.bundle.mediatype.v1: registry+v1
  operators.operatorframework.io.bundle.manifests.v1: manifests/
  operators.operatorframework.io.bundle.metadata.v1: metadata/
  operators.operatorframework.io.bundle.package.v1: $package
  operators.operatorframework.io.bundle.channels.v1: $channel
EOF
	cat >"$bundle_dir/Dockerfile" <<EOF
FROM scratch
LABEL operators.operatorframework.io.bundle.mediatype.v1=registry+v1
LABEL operators.operatorframework.io.bundle.manifests.v1=manifests/
LABEL operators.operatorframework.io.bundle.metadata.v1=metadata/
LABEL operators.operatorframework.io.bundle.package.v1=$package
LABEL operators.operatorframework.io.bundle.channels.v1=$channel
COPY manifests /manifests/
COPY metadata /metadata/
EOF
	image="$push_host/library-olm-fixture-$package:latest"
	docker build -q -t "$image" "$bundle_dir"
	docker save "$image" -o "$bundle_dir/image.tar"
	SSL_CERT_FILE="$registry_ca" "$CRANE" push "$bundle_dir/image.tar" "$image"
	jq -n --arg package "$package" --arg channel "$channel" \
		'{schema:"olm.package",name:$package,defaultChannel:$channel}' \
		>"$catalog_dir/configs/$package-package.json"
	jq -n --arg package "$package" --arg channel "$channel" --arg name "$csv_name" \
		'{schema:"olm.channel",package:$package,name:$channel,entries:[{name:$name}]}' \
		>"$catalog_dir/configs/$package-channel.json"
	jq -n --arg package "$package" --arg name "$csv_name" --arg image "$registry_host/library-olm-fixture-$package:latest" --argjson properties "$properties" \
		'{schema:"olm.bundle",name:$name,package:$package,image:$image,properties:$properties}' \
		>"$catalog_dir/configs/$package-bundle.json"
done
cat >"$catalog_dir/Dockerfile" <<'EOF'
FROM scratch
LABEL operators.operatorframework.io.index.configs.v1=/configs
COPY configs /configs
EOF
catalog_image="$push_host/library-olm-fixture-catalog:latest"
docker build -q -t "$catalog_image" "$catalog_dir"
docker save "$catalog_image" -o "$catalog_dir/image.tar"
SSL_CERT_FILE="$registry_ca" "$CRANE" push "$catalog_dir/image.tar" "$catalog_image"
