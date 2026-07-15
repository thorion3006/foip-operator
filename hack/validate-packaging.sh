#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHART="$ROOT/charts/foip-operator"
FIXTURE="$ROOT/examples/helm/values-safe.yaml"
NODE_HEALTH_FIXTURE="$ROOT/examples/helm/values-node-health-only.yaml"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

command -v helm >/dev/null || { echo "helm is required" >&2; exit 1; }

echo "== Helm lint =="
helm lint "$CHART"

echo "== Render default and safe example values =="
helm template smoke "$CHART" > "$TMP/default.yaml"
helm template smoke "$CHART" -f "$FIXTURE" > "$TMP/safe.yaml"
helm template smoke "$CHART" -f "$NODE_HEALTH_FIXTURE" > "$TMP/node-health-only.yaml"
helm package "$CHART" --version "0.3.0" --app-version "0.3.0" --destination "$TMP" >/dev/null
test -s "$TMP/foip-operator-0.3.0.tgz"

assert_contains() {
  local file=$1
  local text=$2
  grep -Fq -- "$text" "$file" || {
    echo "Missing '$text' in $file" >&2
    exit 1
  }
}

assert_not_contains() {
  local file=$1
  local text=$2
  ! grep -Fq -- "$text" "$file" || {
    echo "Unexpected '$text' in $file" >&2
    exit 1
  }
}

# The example is intentionally installable without a probe: node health alone
# remains a valid and safe baseline for a first deployment.
assert_contains "$TMP/safe.yaml" "name: node-health-only"
assert_contains "$TMP/safe.yaml" "ip: 192.0.2.10"
assert_contains "$TMP/safe.yaml" "probeComposition: All"

# In the minimal fixture, the FailoverIp object has no probe list at all.
if awk '
  /^kind: FailoverIp$/ { in_failover_ip = 1; next }
  in_failover_ip && /^kind: / { exit found }
  in_failover_ip && /^  probes:/ { found = 1 }
  END { exit found }
' "$TMP/node-health-only.yaml"; then
  :
else
  echo "node-health-only example unexpectedly rendered probes" >&2
  exit 1
fi

# Rendered chart must carry every provider-neutral probe kind and the composed
# FailoverIp reference used by the documentation examples.
for probe in tcp-service https-service kubernetes-service; do
  assert_contains "$TMP/safe.yaml" "name: $probe"
done
for type in 'type: TCP' 'type: HTTPS' 'type: Kubernetes'; do
  assert_contains "$TMP/safe.yaml" "$type"
done
assert_contains "$TMP/safe.yaml" "probes:"
assert_contains "$TMP/safe.yaml" "name: tcp-service"
assert_contains "$TMP/safe.yaml" "name: https-service"
assert_contains "$TMP/safe.yaml" "name: kubernetes-service"

# Packaging defaults must not silently weaken the pod or secret boundary.
assert_contains "$TMP/default.yaml" "replicas: 2"
assert_contains "$TMP/default.yaml" "minAvailable: 1"
assert_contains "$TMP/default.yaml" "readOnlyRootFilesystem: true"
assert_contains "$TMP/default.yaml" "allowPrivilegeEscalation: false"
assert_contains "$TMP/default.yaml" "runAsNonRoot: true"
assert_contains "$TMP/default.yaml" "drop:"
assert_contains "$TMP/default.yaml" "- ALL"
assert_contains "$TMP/default.yaml" "add:"
assert_contains "$TMP/default.yaml" "- NET_ADMIN"
assert_contains "$TMP/default.yaml" "limits:"
assert_contains "$TMP/default.yaml" "resources:"
assert_not_contains "$TMP/default.yaml" ":latest"
assert_contains "$TMP/safe.yaml" "resourceNames:"
assert_contains "$TMP/safe.yaml" "netcup-scp-credentials"
assert_contains "$TMP/safe.yaml" "probe-credentials"

# A release must use a pinned chart/image version and publish index metadata;
# the actual registry digest and architecture check runs in the release job.
WORKFLOW="$ROOT/.github/workflows/release.yml"
assert_contains "$WORKFLOW" "./hack/validate-packaging.sh"
assert_contains "$WORKFLOW" "--annotation \"index:org.opencontainers.image.source=https://github.com/thorion3006/foip-operator\""
assert_contains "$WORKFLOW" '--annotation "index:org.opencontainers.image.version=${{ needs.prepare.outputs.version }}"'
assert_contains "$WORKFLOW" "Verify published image architectures"
assert_contains "$WORKFLOW" "platform.architecture == \"amd64\""
assert_contains "$WORKFLOW" "platform.architecture == \"arm64\""
assert_contains "$WORKFLOW" "helm package charts/foip-operator"
assert_contains "$WORKFLOW" 'helm push dist/foip-operator-${{ needs.prepare.outputs.version }}.tgz'

echo "Packaging and release readiness checks passed"
