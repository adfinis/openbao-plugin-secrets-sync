#!/usr/bin/env bash
set -euo pipefail

# Run this helper from the protected main checkout before checking out release
# sources. TRUSTED_COMMIT is the workflow's pinned github.sha, not a tag input.
: "${VERSION:?VERSION is required}"
: "${TRUSTED_COMMIT:?TRUSTED_COMMIT is required}"
export GIT_NO_REPLACE_OBJECTS=1

fail() {
  printf 'release verification failed: %s\n' "$*" >&2
  exit 1
}

[[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || fail "invalid release version"
[[ "${TRUSTED_COMMIT}" =~ ^[0-9a-f]{40}$ ]] || fail "trusted commit must be a full commit ID"
[[ "$(git cat-file -t "${TRUSTED_COMMIT}")" == commit ]] || fail "trusted object must be a commit"
if [[ -n "${EXPECTED_COMMIT:-}" ]]; then
  [[ "${EXPECTED_COMMIT}" =~ ^[0-9a-f]{40}$ ]] || fail "expected commit must be a full commit ID"
fi

tag_object="$(git rev-parse --verify "refs/tags/${VERSION}")"
[[ "$(git cat-file -t "${tag_object}")" == tag ]] || fail "release tag must be annotated"

temp_dir="$(mktemp -d)"
cleanup() {
  gpgconf --homedir "${temp_dir}/gnupg" --kill all >/dev/null 2>&1 || true
  rm -rf "${temp_dir}"
}
trap cleanup EXIT
git cat-file tag "${tag_object}" > "${temp_dir}/tag"
# Only read the tag headers; the signed message can contain arbitrary text.
tag_header="$(sed '/^$/q' "${temp_dir}/tag")"
tag_commit="$(printf '%s\n' "${tag_header}" | sed -n 's/^object //p')"
tag_type="$(printf '%s\n' "${tag_header}" | sed -n 's/^type //p')"
tag_name="$(printf '%s\n' "${tag_header}" | sed -n 's/^tag //p')"
[[ "${tag_name}" == "${VERSION}" ]] || fail "signed tag name does not match requested version"
[[ "${tag_type}" == commit && "${tag_commit}" =~ ^[0-9a-f]{40}$ ]] || fail "tag must point directly to a commit"
[[ "$(git cat-file -t "${tag_commit}")" == commit ]] || fail "tag target must be a commit"
git merge-base --is-ancestor "${tag_commit}" "${TRUSTED_COMMIT}" || fail "tag commit is outside trusted main history"
if [[ -n "${EXPECTED_COMMIT:-}" && "${tag_commit}" != "${EXPECTED_COMMIT}" ]]; then
  fail "tag commit does not match dispatch commit"
fi
manifest_version="$(git show "${tag_commit}:.release-please-manifest.json" | jq -er '."."')"
[[ "${manifest_version}" == "${VERSION}" ]] || fail "release manifest version does not match tag"

# Read keys from the pinned protected-main object, never from candidate sources.
git show "${TRUSTED_COMMIT}:.github/release-signing-keys.asc" > "${temp_dir}/trusted.asc" || fail "cannot read trusted public keys"
if grep -q 'PRIVATE KEY' "${temp_dir}/trusted.asc"; then
  fail "trust file must contain public keys only"
fi
export GNUPGHOME="${temp_dir}/gnupg"
mkdir -m 700 "${GNUPGHOME}"
export RELEASE_VERIFY_GPG
RELEASE_VERIFY_GPG="$(command -v gpg)"
cat > "${temp_dir}/gpg-verify" <<'WRAPPER'
#!/usr/bin/env bash
exec "${RELEASE_VERIFY_GPG}" --no-options --batch --no-tty \
  --homedir "${GNUPGHOME}" --no-auto-key-retrieve --auto-key-locate clear "$@"
WRAPPER
chmod 700 "${temp_dir}/gpg-verify"
"${temp_dir}/gpg-verify" --import "${temp_dir}/trusted.asc" >/dev/null 2>&1 || fail "cannot import trusted public keys"
"${temp_dir}/gpg-verify" --with-colons --list-keys > "${temp_dir}/keys"
grep -q '^pub:' "${temp_dir}/keys" || fail "trust file contains no public keys"

if ! git -c gpg.format=openpgp -c "gpg.openpgp.program=${temp_dir}/gpg-verify" \
  verify-tag --raw "${tag_object}" > "${temp_dir}/status" 2>&1; then
  fail "tag signature does not verify against trusted public keys"
fi
grep -q '^\[GNUPG:\] VALIDSIG ' "${temp_dir}/status" || fail "missing valid signature status"
grep -q '^\[GNUPG:\] GOODSIG ' "${temp_dir}/status" || fail "missing good signature status"
if grep -Eq '^\[GNUPG:\] (BADSIG|ERRSIG|EXPSIG|EXPKEYSIG|REVKEYSIG|KEYREVOKED|KEYEXPIRED|SIGEXPIRED) ' "${temp_dir}/status"; then
  fail "tag signature or signing key is invalid, expired, or revoked"
fi

printf 'Verified release %s: tag %s, commit %s\n' "${VERSION}" "${tag_object}" "${tag_commit}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  printf 'commit=%s\ntag_object=%s\n' "${tag_commit}" "${tag_object}" >> "${GITHUB_OUTPUT}"
fi
