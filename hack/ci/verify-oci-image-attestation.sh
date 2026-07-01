#!/usr/bin/env bash

set -euo pipefail

: "${REPO:?REPO is required (owner/repo)}"
: "${VERSION:?VERSION is required}"
: "${OCI_IMAGE_NAME:?OCI_IMAGE_NAME is required}"
: "${OCI_IMAGE_DIGEST:?OCI_IMAGE_DIGEST is required}"

SIGNER_WORKFLOW="${SIGNER_WORKFLOW:-${REPO}/.github/workflows/release.yml}"
SOURCE_REF="${SOURCE_REF:-refs/heads/main}"
CERT_OIDC_ISSUER="${CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
MAX_ATTEMPTS="${MAX_ATTEMPTS:-10}"
RETRY_SECONDS="${RETRY_SECONDS:-6}"
subject="oci://${OCI_IMAGE_NAME}@${OCI_IMAGE_DIGEST}"

attempts=0
while (( attempts < MAX_ATTEMPTS )); do
  attempts=$((attempts + 1))
  if gh attestation verify "${subject}" \
    --repo "${REPO}" \
    --signer-workflow "${SIGNER_WORKFLOW}" \
    --source-ref "${SOURCE_REF}" \
    --cert-oidc-issuer "${CERT_OIDC_ISSUER}" \
    --deny-self-hosted-runners >/dev/null; then
    echo "Verified attestation: ${subject}"
    exit 0
  fi

  if (( attempts >= MAX_ATTEMPTS )); then
    echo "Failed to verify attestation after ${MAX_ATTEMPTS} attempts: ${subject}" >&2
    exit 1
  fi
  sleep "${RETRY_SECONDS}"
done
