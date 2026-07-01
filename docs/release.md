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
checksums.txt
checksums.txt.bundle
```

The release workflow attaches these artifacts to the matching draft GitHub
Release. The draft must already exist before artifacts are built.

## Local Artifact Build

Build release artifacts locally:

```sh
VERSION=0.1.0-preview.1 make release-artifacts
```

Verify checksums:

```sh
(cd dist/release && shasum -a 256 -c checksums.txt)
```

The build embeds version metadata through Go linker flags. Use a clean tree for
release builds so `dirty=false` is meaningful.

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
- requires the matching GitHub Release to already exist and be a draft;
- builds release binaries with deterministic build metadata derived from the
  tagged commit;
- generates and verifies `checksums.txt`;
- signs `checksums.txt` with a keyless cosign signature bundle;
- creates GitHub build-provenance attestations for `checksums.txt` and the
  release binaries on public repositories;
- verifies checksum signatures and public-repository artifact attestations
  before upload;
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
  -version=0.1.0-preview.1 \
  secret openbao-plugin-secrets-sync
```

Mount or tune an existing mount to use the registered version according to the
normal OpenBao plugin lifecycle.

## Deferred Release Hardening

The first release-engineering slice intentionally does not yet implement:

- SBOM generation;
- byte-for-byte rebuild verification;
- container image publishing.

These should be added once the artifact workflow has run successfully and the
repository release process is settled.
