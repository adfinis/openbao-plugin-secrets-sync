#!/usr/bin/env bash
set -euo pipefail

# The protected export workflow supplies the secret. Only public material leaves
# the temporary keyring; this helper creates no Git ref or GitHub release.
: "${OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY:?signing key secret is required}"
: "${PUBLIC_KEY_DIR:?PUBLIC_KEY_DIR is required}"
temp_dir="$(mktemp -d)"
export GNUPGHOME="${temp_dir}/gnupg"
cleanup() {
  gpgconf --homedir "${GNUPGHOME}" --kill all >/dev/null 2>&1 || true
  rm -rf "${temp_dir}"
}
trap cleanup EXIT
mkdir -m 700 "${GNUPGHOME}"
printf '%s' "${OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY}" |
  gpg --no-options --batch --import >/dev/null 2>&1
unset OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY
fingerprints="$(gpg --no-options --batch --with-colons --list-secret-keys |
  awk -F: '$1=="sec" {primary=1; next} primary && $1=="fpr" {print $10; primary=0}')"
if ! [[ "${fingerprints}" =~ ^[0-9A-F]{40}$ ]]; then
  echo 'signing secret must contain exactly one primary secret key' >&2
  exit 1
fi
mkdir -p "${PUBLIC_KEY_DIR}"
gpg --no-options --batch --armor --export "${fingerprints}" > "${PUBLIC_KEY_DIR}/release-signing-keys.asc"
test -s "${PUBLIC_KEY_DIR}/release-signing-keys.asc"
printf '%s\n' "${fingerprints}" > "${PUBLIC_KEY_DIR}/fingerprint.txt"
printf 'Exported public release key: %s\n' "${fingerprints}"
