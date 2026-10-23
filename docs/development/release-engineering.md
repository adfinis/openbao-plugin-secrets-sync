---
verifiedBy:
  - .github/workflows/release-please.yml
  - .github/workflows/release-pr-gate.yml
  - .github/workflows/release-review-event.yml
  - .github/workflows/release-tag.yml
  - .github/workflows/release.yml
  - .github/workflows/release-public-key.yml
  - .github/workflows/ci.yml
  - .github/release-signing-keys.asc
  - hack/ci
  - mk/build.mk
  - mk/config.mk
  - mk/e2e.mk
  - .ci/versions.yaml
  - Dockerfile.oci-plugin
  - release-please-config.json
---

# Release engineering

Maintainers release `openbao-plugin-secrets-sync` through separate readiness,
approval, tagging, artifact, and publication steps. Operators verify and
install the result with
[Verify release artifacts](../get-started/install.md#verify-release-artifacts).

## Release shape

Release Please manages changelog and version bumps through
`.release-please-manifest.json` and `CHANGELOG.md`. It opens release PRs only.
The release-tag workflow creates the signed tag and the draft GitHub Release,
and dispatches the artifact workflow.

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

The artifact workflow attaches these files to the matching draft GitHub
Release and publishes a multi-platform OCI plugin distribution image to:

```text
ghcr.io/adfinis/openbao-plugin-secrets-sync:v<version>
```

The OCI image is an extraction artifact for OpenBao, not a service container.
It contains the static plugin binary at `/openbao-plugin-secrets-sync`.
Release tags must not contain semver build metadata (`+...`) because the
OpenBao OCI plugin version is used directly as the image tag.

## Local artifact build

The normal Go test suite includes offline release verification tests. These
require Git, Bash, GnuPG (`gpg` and `gpgconf`), and `jq`. The tests create
short-lived signing keys in temporary directories and do not use maintainer
credentials.

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

The release process separates release readiness, approval, and publication:

1. `.github/workflows/release-please.yml` opens or updates the release PR with
   the repository `GITHUB_TOKEN` and `skip-github-release: true`. It dispatches
   validation and the metadata-only release-readiness evaluator.
2. `.github/workflows/release-pr-gate.yml` publishes the `Release Readiness`
   commit status on the exact PR head. Ordinary PRs pass without release
   approval. Release PRs must use the same-repository
   `release-please--branches--main` branch, update both `CHANGELOG.md` and
   `.release-please-manifest.json`, and be ready for review. They require the
   `release:ready` label and current-head approval from the account named by
   the `OPENBAO_SECRET_SYNC_RELEASE_REQUIRED_APPROVER` repository variable.
3. `.github/workflows/release-tag.yml` inspects the merged candidate without
   signing secrets or GitHub writes. It reports the version, PR head, and merge
   commit before requesting approval through the protected `release-manual`
   environment. After approval it revalidates those identities, the readiness
   label, and the designated approval before a step receives signing-secret
   values or imports the key. The approval must predate the PR merge.
4. The tag job creates a signed tag and draft GitHub Release, then dispatches
   `.github/workflows/release.yml`. That workflow uploads draft assets and
   publishes the OCI image to GHCR. These effects are included in tag-job
   approval. Publishing the draft GitHub Release remains a separate action.

Approval submitted after merge does not qualify. If a release PR merges without
qualifying current-head approval, prepare a release candidate with a new version
and obtain approval before merging it.

Configure `Release Readiness` as a required status context in the main-branch
ruleset alongside `Core Quality` and `Dependency Review`. Workflow code alone
does not make a status mandatory. Verify the status appears on ordinary and
release PR heads before enabling that requirement. Include an actual Dependabot
`pull_request_target` run and an ordinary fork PR in that qualification; local
tests do not establish the actor's token permissions or required-context
eligibility. Keep the context optional until these hosted checks succeed.
The evaluator job has a separate name so a job check does not collide with the
commit-status context.

The evaluator uses protected-branch tooling and reads PR metadata only. It does
not execute PR code. The permissionless
`.github/workflows/release-review-event.yml` notifier wakes the trusted
default-branch workflow, which resolves the live canonical release PR through
the GitHub API. It reads no artifacts or event-provided commands. Label
removal, review dismissal, changes requested, draft conversion, and source
updates trigger reevaluation. Comments do not revoke an approval; a later
dismissal or changes-requested review does. Approval on an older head does not
qualify. API failures prevent a successful decision.
Metadata events are asynchronous; a status is not an atomic lock on labels or
reviews. The review consumer resolves GitHub-associated release heads before its
serialized status writer marks them pending and performs live validation.
If GitHub supplies no associated head and its API is unavailable, it cannot
invalidate a prior status until metadata access recovers. Verify the
hosted review-event association and required-status behavior before activating
the ruleset requirement. Tag creation therefore evaluates the policy again
after environment approval and immediately before its release actions.

Release Please commits carry the `github-actions[bot]` sign-off configured in
`release-please-config.json`. GITHUB_TOKEN-created PR updates can produce
approval-required pull-request runs. Approve only the inspected candidate's
runs. Explicitly dispatched validation remains useful, but successful job
checks from a dispatch do not by themselves establish that required PR checks
are satisfied. Verify the PR's required contexts and rollup for its current
head. See [GitHub workflow triggers](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow#triggering-a-workflow-from-a-workflow)
and [required status checks](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks#checks-from-some-workflow-jobs-are-not-evaluated).

The release tag workflow requires the signing key secrets:

```text
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PASSPHRASE
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_NAME
OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_EMAIL
```

Set these values as repository Actions secrets. No broad personal access token
is required for the default release path.

### Trusted signing keys

The artifact workflow verifies release tags with the public keys in
`.github/release-signing-keys.asc` from the pinned protected `main` commit
that runs the workflow. It reads neither keys nor verification tooling from
the candidate release. Verification uses an isolated GnuPG keyring with
automatic key retrieval disabled. Unknown, expired, and revoked keys fail
verification.

The trust file must contain the public key that matches
`OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY`. A placeholder or missing
key blocks artifact builds. To bootstrap the trust file:

1. Merge the `Export Release Public Key` workflow into `main`.
2. Run `.github/workflows/release-public-key.yml` on `main`. The job requires
   approval through the protected `release-manual` environment.
3. Download the `release-public-key` artifact. It contains only an armored
   public key and its primary fingerprint. The workflow creates no tag or
   release and has no repository write permissions.
4. Check the workflow commit, run, and fingerprint against the intended
   signing identity. Replace `.github/release-signing-keys.asc` with the
   exported public key through a reviewed pull request.
5. Merge the key before merging the release PR or running artifact recovery.

The export rejects secrets that contain multiple primary secret keys. It
uses a temporary keyring and removes it after export. Do not download or
commit the private signing key.

For planned rotation, add the next public key through review before changing
the signing secrets. Keep previous public keys while their releases need
recovery. Remove a compromised key from the trust file and review affected
releases before recovery. Removing a key blocks future artifact builds for
tags signed only by that key.

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

The release-tag workflow emits that dispatch after it creates the tag or
confirms an existing tag's target commit, then creates or refreshes the
matching draft GitHub Release. The artifact workflow verifies the signature
before building. Existing signed tags are immutable and are never refreshed.

Manual artifact recovery is available through `workflow_dispatch` on `main`
with a tag input. The job waits for the protected `release-manual` environment
before using release permissions. Configure that environment with required reviewers,
protected branches only, and administrator bypass disabled.

The workflow:

- validates the dispatch source or protected manual approval;
- loads verification tooling and trusted keys from the workflow's pinned
  protected `main` commit;
- requires a signed annotated tag with the requested name that points directly
  to a commit in that protected history;
- verifies the PGP signature against the reviewed public keys;
- checks the tagged release manifest version and, for automatic dispatch,
  the expected commit;
- checks out the verified commit ID for the build;
- runs release source gates (`make lint-ci security-ci test test-race fuzz`):
  lint, vulnerability checks, license checks, filesystem scan, unit tests,
  race tests, and fuzz smoke tests;
- requires the matching GitHub Release to already exist and be a draft;
- builds release binaries with deterministic build metadata derived from the
  tagged commit;
- generates per-binary SPDX JSON SBOMs from the compiled Go build metadata;
- generates a dependency license report for the shipped Go package graph;
- rebuilds the binaries and SBOMs independently and verifies byte equality;
- writes a reproducibility report and includes it in `checksums.txt`;
- generates and verifies `checksums.txt`;
- registers and mounts the built release binary in OpenBao and runs the
  self-contained LocalStack smoke test (`make test-e2e-release-localstack`);
- runs the OCI plugin download smoke test against the release binary
  (`make test-e2e-oci-release-localstack`);
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
- uploads the files as the `release-plugin-artifacts-<version>` workflow
  artifact;
- uploads the files to the matching GitHub Release without replacing
  conflicting existing assets;
- refuses to add missing assets to an already published release.

## Publish the draft release

The artifact workflow intentionally leaves the GitHub Release in draft state.
After the workflow succeeds, a maintainer completes these checks before
publishing:

1. Verify the signed tag points to the release PR merge commit.
2. Confirm the release workflow, source gates, LocalStack release-binary smoke
   test, OCI release smoke test, image scan, reproducibility check, and upload
   steps passed.
3. Compare the draft assets with `provenance-index.json`; confirm both binaries,
   both SBOMs, the license report, reproducibility report, checksums, signature
   bundle, and provenance index are present.
4. Verify `checksums.txt`, its cosign bundle, the public build-provenance
   attestations, and the OCI image signature and attestation using
   [Verify release artifacts](../get-started/install.md#verify-release-artifacts).
5. Review the curated and generated release notes, and confirm preview releases
   are marked as prereleases.

After the first OCI upload, check the package visibility. GHCR creates new
packages as private by default. A package administrator must change visibility
to Public before anonymous consumer tests can pass. Public visibility cannot
be reverted. Verify anonymous download before publishing the GitHub Release.
See [GitHub package visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).

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
