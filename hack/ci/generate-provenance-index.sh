#!/usr/bin/env bash

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${OWNER:?OWNER is required}"
: "${VERSION:?VERSION is required}"
: "${SOURCE_DATE_EPOCH:?SOURCE_DATE_EPOCH is required}"

INDEX_PATH="${INDEX_PATH:-dist/release/provenance-index.json}"
CHECKSUMS_PATH="${CHECKSUMS_PATH:-dist/release/checksums.txt}"
CHECKSUMS_BUNDLE_PATH="${CHECKSUMS_BUNDLE_PATH:-dist/release/checksums.txt.bundle}"
SBOM_GLOB="${SBOM_GLOB:-dist/release/sbom-*.spdx.json}"
REPRODUCIBILITY_REPORT_PATH="${REPRODUCIBILITY_REPORT_PATH:-dist/release/reproducibility-report.md}"
BINARY_NAME="${BINARY_NAME:-openbao-plugin-secrets-sync}"
PLUGIN_VERSION="${PLUGIN_VERSION:-v${VERSION}}"
RELEASE_SOURCE_REF="${RELEASE_SOURCE_REF:-refs/tags/${VERSION}}"
RELEASE_WORKFLOW="${RELEASE_WORKFLOW:-${REPO}/.github/workflows/release.yml}"
ATTESTATIONS_AVAILABLE="${ATTESTATIONS_AVAILABLE:-true}"
ATTESTATIONS_UNAVAILABLE_REASON="${ATTESTATIONS_UNAVAILABLE_REASON:-}"
REPRODUCIBLE="${REPRODUCIBLE:-true}"

go run ./hack/tools/provenance_index \
  -index-path "${INDEX_PATH}" \
  -repo "${REPO}" \
  -owner "${OWNER}" \
  -version "${VERSION}" \
  -plugin-version "${PLUGIN_VERSION}" \
  -source-date-epoch "${SOURCE_DATE_EPOCH}" \
  -binary-name "${BINARY_NAME}" \
  -release-source-ref "${RELEASE_SOURCE_REF}" \
  -release-workflow "${RELEASE_WORKFLOW}" \
  -checksums-path "${CHECKSUMS_PATH}" \
  -checksums-bundle-path "${CHECKSUMS_BUNDLE_PATH}" \
  -sbom-glob "${SBOM_GLOB}" \
  -reproducibility-report-path "${REPRODUCIBILITY_REPORT_PATH}" \
  -reproducible="${REPRODUCIBLE}" \
  -attestations-available="${ATTESTATIONS_AVAILABLE}" \
  -attestations-unavailable-reason "${ATTESTATIONS_UNAVAILABLE_REASON}"
