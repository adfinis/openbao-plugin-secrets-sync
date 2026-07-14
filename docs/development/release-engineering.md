# Release engineering

This document describes the maintainer release workflow for
`openbao-plugin-secrets-sync`. Use
[Install and verify release artifacts](../operations/install-and-verify.md)
for operator-facing artifact verification and plugin installation.

## Release shape

Release Please manages changelog and version bumps through
`.release-please-manifest.json` and `CHANGELOG.md`. It opens release PRs only.
Signed tag creation, draft GitHub Release creation, and release artifact
dispatch are handled by the dedicated release-tag workflow.

The release artifact workflow builds Linux plugin binaries for:

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
go-licenses-report.csv
reproducibility-report.md
checksums.txt
checksums.txt.bundle
provenance-index.json
```

The release artifact workflow attaches these artifacts to the matching draft
GitHub Release. The draft must already exist before artifacts are built. It
also publishes a multi-platform OCI plugin distribution image to:

```text
ghcr.io/adfinis/openbao-plugin-secrets-sync:v<version>
```

The OCI image is an extraction artifact for OpenBao, not a service container.
It contains the static plugin binary at `/openbao-plugin-secrets-sync`.
Release tags must not contain semver build metadata (`+...`) because the
OpenBao OCI plugin version is used directly as the image tag.

## Local artifact build

Build release artifacts locally:

```sh
make install-go-tools
VERSION=0.1.0-preview.1 make release-artifacts
```

This creates the Linux plugin binaries, per-binary SPDX JSON SBOMs, a
dependency license report, and `checksums.txt`. Signature bundles and
`provenance-index.json` are generated after checksum verification during the
release workflow.

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
REPO=adfinis/openbao-plugin-secrets-sync OWNER=adfinis \
  VERSION=0.1.0-preview.1 PLUGIN_VERSION=v0.1.0-preview.1 \
  SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH}" \
  bash hack/ci/generate-provenance-index.sh
```

## Release flow

The release process has three separate automation steps:

1. `.github/workflows/release-please.yml` opens or updates the release PR using
   the repository `GITHUB_TOKEN` and `skip-github-release: true`.
2. `.github/workflows/release-pr-gate.yml` requires the `release:ready` label
   and approval from the user configured in the
   `OPENBAO_SECRET_SYNC_RELEASE_REQUIRED_APPROVER` repository variable.
3. `.github/workflows/release-tag.yml` creates a signed annotated semver tag and
   a draft GitHub Release from the merged release PR, then dispatches
   `.github/workflows/release.yml` with a `repository_dispatch` event.

Release Please commits are signed off through `release-please-config.json` with
the `github-actions[bot]` identity. Release PR checks created by
`GITHUB_TOKEN` can require manual approval in the GitHub UI before they run.
Because `GITHUB_TOKEN`-created pull requests do not trigger normal pull request
workflows, the release-please workflow explicitly dispatches the required
`Core Quality` and `Dependency Review` checks against the release PR branch.

The release tag workflow requires the signing key secrets:

```text
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PASSPHRASE
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_NAME
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_EMAIL
```

Set these values as repository Actions secrets. No broad personal access token
is required for the default release path.

The tag ruleset protects semver tags from update and deletion. Automated tag
creation is performed by `GITHUB_TOKEN` in the release-tag workflow. For
GitHub App-based release automation, restrict semver tag creation to the
release tag app and keep the release PR and tag identities separate.

## Artifact workflow

The workflow in `.github/workflows/release.yml` runs on the internal
`release-artifacts` repository dispatch event:

```text
repository_dispatch: release-artifacts
```

The release-tag workflow emits that dispatch only after it has created or
verified the signed tag and created or refreshed the matching draft GitHub
Release. Existing signed tags are immutable and are never refreshed.

Manual artifact recovery is available through `workflow_dispatch` with a tag
input, but the job waits for the protected `release-manual` environment before
using release permissions. Configure that environment with required reviewers,
protected branches only, and administrator bypass disabled.

The workflow:

- validates the dispatch source or protected manual approval;
- checks out the tag;
- requires an annotated tag with a PGP signature block;
- runs release source gates: lint, vulnerability checks, license checks,
  filesystem scan, unit tests, race tests, and fuzz smoke tests;
- requires the matching GitHub Release to already exist and be a draft;
- builds release binaries with deterministic build metadata derived from the
  tagged commit;
- generates per-binary SPDX JSON SBOMs from the compiled Go build metadata;
- generates a dependency license report for the shipped Go package graph;
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

## Publish the draft release

The artifact workflow intentionally leaves the GitHub Release in draft state.
After the workflow succeeds, a maintainer completes these checks before
publishing:

1. Verify the signed tag points to the release PR merge commit.
2. Confirm the release workflow, source gates, LocalStack release-binary smoke
   test, OCI e2e, image scan, reproducibility check, and upload steps passed.
3. Compare the draft assets with `provenance-index.json`; confirm both binaries,
   both SBOMs, the license report, reproducibility report, checksums, signature
   bundle, and provenance index are present.
4. Verify `checksums.txt`, its cosign bundle, the public build-provenance
   attestations, and the OCI image signature and attestation using
   [Install and verify release artifacts](../operations/install-and-verify.md).
5. Review the curated and generated release notes, and confirm preview releases
   are marked as prereleases.

Publish only after the draft is complete. For the first preview:

```sh
VERSION=0.1.0-preview.1
REPO=adfinis/openbao-plugin-secrets-sync

gh release view "${VERSION}" --repo "${REPO}" \
  --json tagName,isDraft,isPrerelease,name,url

gh release edit "${VERSION}" --repo "${REPO}" \
  --verify-tag --draft=false --prerelease
```

Published releases are immutable from the release workflow's perspective. If
an artifact is missing or inconsistent, leave the release as a draft, diagnose
the failed workflow, and use the protected manual recovery path rather than
publishing an incomplete release.

## License metadata

The plugin source code is licensed under Apache-2.0. Release binaries also link
OpenBao, HashiCorp-derived, provider, and telemetry modules that retain their
own licenses, including MPL-2.0 modules. The release workflow verifies the
configured dependency license allow-list and publishes
`go-licenses-report.csv` so operators can inspect the package-level license
evidence that accompanied the release.

The OCI image label `org.opencontainers.image.licenses` records the plugin
project license as `Apache-2.0`. Dependency license evidence remains in the
release license report and SBOMs rather than being flattened into the OCI image
label.

This repository is an external OpenBao plugin released separately from OpenBao
core, matching OpenBao's documented model for external, community-supported
plugins:
<https://openbao.org/community/policies/plugins/>.
