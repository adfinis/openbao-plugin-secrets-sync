#!/usr/bin/env bash

set -euo pipefail

: "${REPO:?REPO is required in owner/repo form}"
: "${VERSION:?VERSION is required}"

CHECKSUMS_PATH="${CHECKSUMS_PATH:-dist/release/checksums.txt}"
SIGNER_WORKFLOW="${SIGNER_WORKFLOW:-${REPO}/.github/workflows/release.yml}"
SOURCE_REF="${SOURCE_REF:-refs/heads/main}"
CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_SECONDS="${RETRY_SECONDS:-6}"

if [[ ! -f "${CHECKSUMS_PATH}" ]]; then
  echo "checksums file not found: ${CHECKSUMS_PATH}" >&2
  exit 1
fi

verify_file_subject() {
  local file_path="$1"
  local attempts=0

  while (( attempts < MAX_ATTEMPTS )); do
    attempts=$((attempts + 1))
    if gh attestation verify "${file_path}" \
      --repo "${REPO}" \
      --signer-workflow "${SIGNER_WORKFLOW}" \
      --source-ref "${SOURCE_REF}" \
      --cert-oidc-issuer "${CERT_OIDC_ISSUER}" \
      --deny-self-hosted-runners >/dev/null; then
      echo "Verified attestation: ${file_path}"
      return 0
    fi

    if (( attempts >= MAX_ATTEMPTS )); then
      echo "Failed to verify attestation after ${MAX_ATTEMPTS} attempts: ${file_path}" >&2
      return 1
    fi
    sleep "${RETRY_SECONDS}"
  done
}

verify_file_subject "${CHECKSUMS_PATH}"

while read -r _ name; do
  [[ -n "${name}" ]] || continue
  path="$(dirname "${CHECKSUMS_PATH}")/${name}"
  if [[ -f "${path}" ]]; then
    verify_file_subject "${path}"
  fi
done < "${CHECKSUMS_PATH}"
