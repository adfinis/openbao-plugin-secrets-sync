#!/usr/bin/env bash

set -euo pipefail

: "${VERSION:?VERSION is required}"
: "${COMMIT:?COMMIT is required}"
: "${BUILD_DATE:?BUILD_DATE is required}"

PRIMARY_DIR="${PRIMARY_DIR:-dist/release}"
REBUILD_DIR="${REBUILD_DIR:-dist/rebuild}"
REPORT_PATH="${REPORT_PATH:-${PRIMARY_DIR}/reproducibility-report.md}"
MIRROR_REPORT_PATH="${MIRROR_REPORT_PATH:-}"

if [[ ! -f "${PRIMARY_DIR}/checksums.txt" ]]; then
  echo "primary checksums file not found: ${PRIMARY_DIR}/checksums.txt" >&2
  exit 1
fi

sha256_file() {
  local path="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
    return
  fi
  shasum -a 256 "${path}" | awk '{print $1}'
}

mkdir -p "$(dirname "${REPORT_PATH}")"

{
  echo "## Reproducibility Report"
  echo
  echo "Version: \`${VERSION}\`"
  echo
  echo "Commit: \`${COMMIT}\`"
  echo
  echo "Build date: \`${BUILD_DATE}\`"
  echo
  echo "| Subject | Primary SHA-256 | Rebuild SHA-256 | Match |"
  echo "| :--- | :--- | :--- | :---: |"
  while read -r _ rel; do
    [[ -n "${rel}" ]] || continue
    primary_path="${PRIMARY_DIR}/${rel}"
    rebuild_path="${REBUILD_DIR}/${rel}"
    if [[ ! -f "${primary_path}" || ! -f "${rebuild_path}" ]]; then
      echo "| ${rel} | missing | missing | no |"
      continue
    fi
    primary_sha="$(sha256_file "${primary_path}")"
    rebuild_sha="$(sha256_file "${rebuild_path}")"
    if [[ "${primary_sha}" == "${rebuild_sha}" ]]; then
      match="yes"
    else
      match="no"
    fi
    echo "| ${rel} | \`${primary_sha}\` | \`${rebuild_sha}\` | ${match} |"
  done < "${PRIMARY_DIR}/checksums.txt"
} > "${REPORT_PATH}"

if [[ -n "${MIRROR_REPORT_PATH}" ]]; then
  mkdir -p "$(dirname "${MIRROR_REPORT_PATH}")"
  cp "${REPORT_PATH}" "${MIRROR_REPORT_PATH}"
fi
