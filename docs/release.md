# Release Engineering

Status: draft
Date: 2026-07-01

This document describes the current release baseline for
`openbao-plugin-secrets-sync`. The project is still early-stage, so this is a
minimum artifact workflow rather than the final supply-chain posture.

## Current Release Shape

Release Please manages changelog and version bumps through
`.release-please-manifest.json` and `CHANGELOG.md`.

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

The release workflow attaches these artifacts to the matching GitHub Release.
If a semver tag exists but the GitHub Release does not, the workflow creates a
draft release and uploads the assets there.

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

## Release Workflow

The workflow in `.github/workflows/release.yml` runs on semver tags:

```text
0.1.0
0.1.0-preview.1
```

It can also be run manually for an existing semver tag through
`workflow_dispatch`.

Early-stage limitation: when a tag is created by another GitHub Actions
workflow using the default `GITHUB_TOKEN`, GitHub may not start the tag-triggered
workflow automatically. In that case, run the release workflow manually with the
existing tag. A later hardening pass should move release PR and tag creation to
a dedicated GitHub App or equivalent release identity.

The workflow:

- checks out the tag;
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

- signed release tags;
- release PR approval gates;
- SBOM generation;
- byte-for-byte rebuild verification;
- container image publishing.

These should be added once the artifact workflow has run successfully and the
repository release process is settled.
