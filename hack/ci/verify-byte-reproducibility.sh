#!/usr/bin/env bash

set -euo pipefail

PRIMARY_DIR="${PRIMARY_DIR:-dist/release}"
REBUILD_DIR="${REBUILD_DIR:-dist/rebuild}"

if [[ ! -d "${PRIMARY_DIR}" ]]; then
  echo "primary artifact directory not found: ${PRIMARY_DIR}" >&2
  exit 1
fi
if [[ ! -d "${REBUILD_DIR}" ]]; then
  echo "rebuild artifact directory not found: ${REBUILD_DIR}" >&2
  exit 1
fi
if [[ ! -f "${PRIMARY_DIR}/checksums.txt" ]]; then
  echo "primary checksums file not found: ${PRIMARY_DIR}/checksums.txt" >&2
  exit 1
fi

status=0

sha256_file() {
  local path="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
    return
  fi
  shasum -a 256 "${path}" | awk '{print $1}'
}

compare_file() {
  local rel="$1"
  local primary_path="${PRIMARY_DIR}/${rel}"
  local rebuild_path="${REBUILD_DIR}/${rel}"

  if [[ ! -f "${primary_path}" || ! -f "${rebuild_path}" ]]; then
    echo "required file missing for reproducibility check: ${rel}" >&2
    status=1
    return
  fi

  local primary_sha rebuild_sha
  primary_sha="$(sha256_file "${primary_path}")"
  rebuild_sha="$(sha256_file "${rebuild_path}")"

  if [[ "${primary_sha}" != "${rebuild_sha}" ]]; then
    echo "byte mismatch (${rel}): primary=${primary_sha} rebuild=${rebuild_sha}" >&2
    status=1
    return
  fi
  echo "byte match (${rel}): ${primary_sha}"
}

compare_file "checksums.txt"
while read -r _ rel; do
  [[ -n "${rel}" ]] || continue
  compare_file "${rel}"
done < "${PRIMARY_DIR}/checksums.txt"

if (( status != 0 )); then
  echo "byte reproducibility verification failed" >&2
  exit 1
fi

echo "byte reproducibility verification passed"
