#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION=$(tr -d '[:space:]' < "$ROOT/VERSION")

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

assert_contains "$ROOT/charts/foip-operator/Chart.yaml" "version: $VERSION"
assert_contains "$ROOT/charts/foip-operator/Chart.yaml" "appVersion: $VERSION"
assert_contains "$ROOT/Makefile" 'RELEASE_VERSION ?= $(shell cat VERSION'
assert_contains "$ROOT/Makefile" 'CHART_VERSION ?= $(RELEASE_VERSION)'
assert_contains "$ROOT/Makefile" 'version-check: ## Verify versioned files match VERSION.'
assert_contains "$ROOT/examples/helm/values-safe.yaml" "tag: \"$VERSION\""
assert_contains "$ROOT/examples/helm/values-node-health-only.yaml" "tag: \"$VERSION\""
assert_contains "$ROOT/README.md" "releases/tag/v$VERSION"
assert_contains "$ROOT/README.md" "service.version=$VERSION,foip.component=foip"
assert_not_contains "$ROOT/README.md" "compare/v0.3.0...v1.0.0"

echo "Version sync checks passed"
