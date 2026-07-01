# Release Engineering

Status: draft
Date: 2026-07-01

This document describes the current release baseline for
`openbao-plugin-secrets-sync`. The project is still early-stage, so this is a
minimum artifact workflow rather than the final supply-chain posture.

## Current Release Shape

Release Please manages changelog and version bumps through
`.release-please-manifest.json` and `CHANGELOG.md`. It opens release PRs only;
tag creation and draft GitHub Release creation are handled by the dedicated
release-tag workflow.

Tag-triggered releases build Linux plugin binaries for:

```text
linux/amd64
linux/arm64
```

Published artifacts use this naming shape:

```text
openbao-plugin-secrets-sync_<version>_linux_amd64
openbao-plugin-secrets-sync_<version>_linux_arm64
sbom-openbao-plugin-secrets-sync-linux-amd64.spdx.json
sbom-openbao-plugin-secrets-sync-linux-arm64.spdx.json
reproducibility-report.md
checksums.txt
checksums.txt.bundle
provenance-index.json
```

The release workflow attaches these artifacts to the matching draft GitHub
Release. The draft must already exist before artifacts are built. It also
publishes a multi-platform OCI plugin distribution image to:

```text
ghcr.io/adfinis/openbao-secret-sync:v<version>
```

The OCI image is an extraction artifact for OpenBao, not a service container.
It contains the static plugin binary at `/openbao-plugin-secrets-sync`.
Release tags must not contain semver build metadata (`+...`) because the
OpenBao OCI plugin version is used directly as the image tag.

## Local Artifact Build

Build release artifacts locally:

```sh
VERSION=0.1.0-preview.1 make release-artifacts
```

This creates the Linux plugin binaries, per-binary SPDX JSON SBOMs, and
`checksums.txt`. Signature bundles and `provenance-index.json` are generated
after checksum verification during the release workflow.

Verify checksums:

```sh
(cd dist/release && shasum -a 256 -c checksums.txt)
```

Build the local OCI plugin image from the release binaries:

```sh
VERSION=0.1.0-preview.1 make oci-plugin-image
```

The build embeds version metadata through Go linker flags. Use a clean tree for
release builds so `dirty=false` is meaningful. Release tags and artifact names
omit the leading `v`, but the plugin reports a `v`-prefixed runtime version to
match OpenBao plugin catalog version normalization.

To exercise the same reproducibility path locally, build with fixed metadata
into two directories, compare them, write the report into both directories, and
regenerate checksums:

```sh
SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
BUILD_DATE="$(
  date -u -r "${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null ||
    date -u -d "@${SOURCE_DATE_EPOCH}" +%Y-%m-%dT%H:%M:%SZ
)"
COMMIT="$(git rev-parse HEAD)"

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH}" make release-artifacts \
  DIST_DIR=dist/release VERSION=0.1.0-preview.1 COMMIT="${COMMIT}" \
  BUILD_DATE="${BUILD_DATE}" DIRTY=false

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH}" make release-artifacts \
  DIST_DIR=dist/rebuild VERSION=0.1.0-preview.1 COMMIT="${COMMIT}" \
  BUILD_DATE="${BUILD_DATE}" DIRTY=false

PRIMARY_DIR=dist/release REBUILD_DIR=dist/rebuild \
  bash hack/ci/verify-byte-reproducibility.sh

VERSION=0.1.0-preview.1 COMMIT="${COMMIT}" BUILD_DATE="${BUILD_DATE}" \
  PRIMARY_DIR=dist/release REBUILD_DIR=dist/rebuild \
  REPORT_PATH=dist/release/reproducibility-report.md \
  MIRROR_REPORT_PATH=dist/rebuild/reproducibility-report.md \
  bash hack/ci/write-reproducibility-report.sh

make checksums DIST_DIR=dist/release
make checksums DIST_DIR=dist/rebuild

PRIMARY_DIR=dist/release REBUILD_DIR=dist/rebuild \
  bash hack/ci/verify-byte-reproducibility.sh
```

After signing `checksums.txt`, generate the local provenance index:

```sh
REPO=adfinis/openbao-secret-sync OWNER=adfinis \
  VERSION=0.1.0-preview.1 PLUGIN_VERSION=v0.1.0-preview.1 \
  SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH}" \
  bash hack/ci/generate-provenance-index.sh
```

## Release Flow

The release process has three separate automation steps:

1. `.github/workflows/release-please.yml` opens or updates the release PR using
   a GitHub App token and `skip-github-release: true`.
2. `.github/workflows/release-pr-gate.yml` requires the `release:ready` label
   and approval from the user configured in the
   `OPENBAO_SECRET_SYNC_RELEASE_REQUIRED_APPROVER` repository variable.
3. `.github/workflows/release-tag.yml` creates a signed annotated semver tag and
   a draft GitHub Release from the merged release PR.

The release PR app requires:

```text
OPENBAO_SECRET_SYNC_RELEASE_PR_APP_ID
OPENBAO_SECRET_SYNC_RELEASE_PR_PRIVATE_KEY
```

The release tag app and signing key require:

```text
OPENBAO_SECRET_SYNC_RELEASE_TAG_APP_ID
OPENBAO_SECRET_SYNC_RELEASE_TAG_PRIVATE_KEY
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PASSPHRASE
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_NAME
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_EMAIL
```

## Artifact Workflow

The workflow in `.github/workflows/release.yml` runs on semver tags:

```text
0.1.0
0.1.0-preview.1
```

It can also be run manually for an existing semver tag through
`workflow_dispatch`.

The workflow:

- checks out the tag;
- runs release source gates: lint, vulnerability checks, license checks,
  filesystem scan, unit tests, race tests, and fuzz smoke tests;
- requires the matching GitHub Release to already exist and be a draft;
- builds release binaries with deterministic build metadata derived from the
  tagged commit;
- generates per-binary SPDX JSON SBOMs from the compiled Go build metadata;
- rebuilds the binaries and SBOMs independently and verifies byte equality;
- writes a reproducibility report and includes it in `checksums.txt`;
- generates and verifies `checksums.txt`;
- registers and mounts the built release binary in OpenBao and runs the
  self-contained LocalStack smoke test;
- publishes a minimal multi-platform OCI plugin image to GHCR using the
  `v`-prefixed OpenBao plugin version as the image tag;
- scans, signs, and verifies the OCI plugin image by digest;
- signs `checksums.txt` with a keyless cosign signature bundle;
- creates GitHub build-provenance attestations for `checksums.txt` and the
  release binaries on public repositories;
- creates a registry-pushed provenance attestation for the OCI plugin image on
  public repositories;
- verifies checksum signatures, public-repository artifact attestations, and
  public-repository OCI image attestations before upload;
- writes `provenance-index.json` with release identity, checksum evidence,
  binary assets, SBOMs, reproducibility status, OCI image digest, and
  attestation availability;
- uploads the files as workflow artifacts;
- uploads the files to the matching GitHub Release without replacing
  conflicting existing assets;
- refuses to add missing assets to an already published release.

## Operator Verification

Download the artifact for the target platform and `checksums.txt`, then verify:

```sh
shasum -a 256 -c checksums.txt
```

Verify the checksum file signature with `cosign`:

```sh
VERSION=0.1.0-preview.1
REPO=adfinis/openbao-secret-sync
WORKFLOW_IDENTITY="https://github.com/${REPO}/.github/workflows/release.yml@refs/tags/${VERSION}"

cosign verify-blob \
  --new-bundle-format=true \
  --bundle checksums.txt.bundle \
  --certificate-identity "${WORKFLOW_IDENTITY}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

For public releases, verify the artifact provenance attestation against the
release workflow identity:

```sh
gh attestation verify "./openbao-plugin-secrets-sync_${VERSION}_linux_amd64" \
  --repo "${REPO}" \
  --signer-workflow "${REPO}/.github/workflows/release.yml" \
  --source-ref "refs/tags/${VERSION}" \
  --cert-oidc-issuer https://token.actions.githubusercontent.com \
  --deny-self-hosted-runners
```

Inspect the release provenance index:

```sh
jq '.release, .checksums, .assets, .reproducibility, .attestations' \
  provenance-index.json
```

Install the binary into the OpenBao plugin directory under the command name used
at registration time:

```sh
install -m 0755 \
  openbao-plugin-secrets-sync_0.1.0-preview.1_linux_amd64 \
  /opt/openbao/plugins/openbao-plugin-secrets-sync
```

Register the plugin with the checksum of the installed binary:

```sh
sha256="$(shasum -a 256 /opt/openbao/plugins/openbao-plugin-secrets-sync | awk '{print $1}')"

bao plugin register \
  -sha256="$sha256" \
  -command=openbao-plugin-secrets-sync \
  -version=v0.1.0-preview.1 \
  secret openbao-plugin-secrets-sync
```

Mount or tune an existing mount to use the registered version according to the
normal OpenBao plugin lifecycle.

## OCI Plugin Images

OpenBao supports OCI-based plugin distribution through declarative `plugin`
configuration. In that model OpenBao downloads an OCI image, extracts the
plugin binary from the image root, verifies the extracted binary SHA-256, and
runs the binary as a normal external plugin process.

The release workflow publishes the OCI plugin image as:

```text
ghcr.io/adfinis/openbao-secret-sync:v0.1.0-preview.1
```

Use the image digest from `provenance-index.json` for verification and
deployment records:

```sh
jq -r '.oci_plugin_image.ref, .oci_plugin_image.digest' provenance-index.json
```

Verify the OCI image signature by digest:

```sh
IMAGE_NAME=ghcr.io/adfinis/openbao-secret-sync
IMAGE_DIGEST=sha256:<digest-from-provenance-index>

cosign verify \
  --new-bundle-format=true \
  --certificate-identity "${WORKFLOW_IDENTITY}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "${IMAGE_NAME}@${IMAGE_DIGEST}"
```

For public releases, verify the OCI image provenance attestation:

```sh
gh attestation verify "oci://${IMAGE_NAME}@${IMAGE_DIGEST}" \
  --repo "${REPO}" \
  --signer-workflow "${REPO}/.github/workflows/release.yml" \
  --source-ref "refs/tags/${VERSION}" \
  --cert-oidc-issuer https://token.actions.githubusercontent.com \
  --deny-self-hosted-runners
```

Configure OpenBao to download and register the OCI plugin:

```hcl
plugin_directory = "/opt/openbao/plugins"
plugin_auto_download = true
plugin_auto_register = true
plugin_download_behavior = "fail"
plugin_download_max_size = 134217728 # 128 MiB, expressed as bytes.

plugin "secret" "openbao-plugin-secrets-sync" {
  image       = "ghcr.io/adfinis/openbao-secret-sync"
  version     = "v0.1.0-preview.1"
  binary_name = "openbao-plugin-secrets-sync"
  sha256sum   = "<openbao_plugin_catalog_sha256 from provenance-index.json>"
}
```

`sha256sum` is the checksum of the extracted plugin binary, not the OCI image
digest. The binary checksum is recorded per platform in
`provenance-index.json` under the matching release asset as
`openbao_plugin_catalog_sha256`.

## Deferred Release Hardening

The release workflow includes a self-contained OpenBao OCI-download e2e smoke
test before publishing. Remaining release confidence still depends on the first
real tag run in GitHub, because local tests cannot prove GHCR permissions,
keyless signing, registry-pushed attestations, or GitHub Release upload
behavior.
