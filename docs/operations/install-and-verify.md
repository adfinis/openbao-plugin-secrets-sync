# Install and verify release artifacts

This document describes how to verify released Secret Sync artifacts and
install the plugin into OpenBao. Use
[Release engineering](../development/release-engineering.md) for maintainer
release automation.

## Compatibility

The preview release is qualified against OpenBao `2.5.5`. Other OpenBao
versions are not yet qualified. Preview releases do not establish API, storage,
provider-metadata, upgrade, downgrade, or migration compatibility across
versions.

## Artifact set

Release artifacts include Linux plugin binaries, per-binary SBOMs, a dependency
license report, checksums, a keyless checksum signature bundle, a
reproducibility report, and a provenance index.

Published binary names use this shape:

```text
openbao-plugin-secrets-sync_<version>_linux_amd64
openbao-plugin-secrets-sync_<version>_linux_arm64
```

The OCI plugin distribution image is published as:

```text
ghcr.io/adfinis/openbao-plugin-secrets-sync:v<version>
```

The OCI image is an extraction artifact for OpenBao, not a service container.
It contains the static plugin binary at `/openbao-plugin-secrets-sync`.

## Verify binary artifacts

Download the artifact for the target platform and `checksums.txt`, then verify:

```sh
shasum -a 256 -c checksums.txt
```

Verify the checksum file signature with `cosign`:

```sh
VERSION=0.1.0-preview.1
REPO=adfinis/openbao-plugin-secrets-sync
WORKFLOW_REF=refs/heads/main
WORKFLOW_IDENTITY="https://github.com/${REPO}/.github/workflows/release.yml@${WORKFLOW_REF}"

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
  --source-ref "${WORKFLOW_REF}" \
  --cert-oidc-issuer https://token.actions.githubusercontent.com \
  --deny-self-hosted-runners
```

Inspect the release provenance index:

```sh
jq '.release, .checksums, .assets, .reproducibility, .attestations' \
  provenance-index.json
```

Inspect dependency license evidence:

```sh
column -s, -t go-licenses-report.csv | less -S
```

The plugin project source is licensed under Apache-2.0. Dependency licenses,
including MPL-2.0 OpenBao modules, remain attached to the corresponding
packages and are listed in the license report.

## Install a binary plugin

Install the binary into the OpenBao plugin directory under the command name
used at registration time:

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

## Verify the OCI plugin image

Use the image digest from `provenance-index.json` for verification and
deployment records:

```sh
jq -r '.oci_plugin_image.ref, .oci_plugin_image.digest' provenance-index.json
```

Verify the OCI image signature by digest:

```sh
IMAGE_NAME=ghcr.io/adfinis/openbao-plugin-secrets-sync
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
  --source-ref "${WORKFLOW_REF}" \
  --cert-oidc-issuer https://token.actions.githubusercontent.com \
  --deny-self-hosted-runners
```

## Configure OCI plugin download

OpenBao supports OCI-based plugin distribution through declarative `plugin`
configuration. In that model OpenBao downloads an OCI image, extracts the
plugin binary from the image root, verifies the extracted binary SHA-256, and
runs the binary as a normal external plugin process.

Configure OpenBao to download and register the OCI plugin:

```hcl
plugin_directory = "/opt/openbao/plugins"
plugin_auto_download = true
plugin_auto_register = true
plugin_download_behavior = "fail"
plugin_download_max_size = 134217728 # 128 MiB, expressed as bytes.

plugin "secret" "openbao-plugin-secrets-sync" {
  image       = "ghcr.io/adfinis/openbao-plugin-secrets-sync"
  version     = "v0.1.0-preview.1"
  binary_name = "openbao-plugin-secrets-sync"
  sha256sum   = "<openbao_plugin_catalog_sha256 from provenance-index.json>"
}
```

`sha256sum` is the checksum of the extracted plugin binary, not the OCI image
digest. The binary checksum is recorded per platform in `provenance-index.json`
under the matching release asset as `openbao_plugin_catalog_sha256`.
