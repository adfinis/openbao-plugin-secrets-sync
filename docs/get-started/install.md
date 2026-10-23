---
verifiedBy:
  - mk/build.mk
  - mk/config.mk
  - Dockerfile.oci-plugin
  - .github/workflows/release.yml
  - hack/ci/verify-release-artifact-attestations.sh
  - hack/ci/verify-oci-image-attestation.sh
  - hack/tools/provenance_index/main.go
  - hack/e2e/prepare-oci-fixture.sh
  - test/e2e/localstack/secret_sync_e2e_test.go
  - internal/backend/path_config.go
---

# Install the plugin

Install a verified Secret Sync release into OpenBao, register it in the plugin
catalog, and mount it at `secret-sync/`. Check
[Supported OpenBao versions](../reference/compatibility.md#supported-openbao-versions)
before you start.

You need:

- an OpenBao token that can register plugins and enable secrets engines;
- write access to the OpenBao `plugin_directory`, or permission to change the
  OpenBao server configuration for OCI plugin download; and
- `cosign`, `gh`, `jq`, and `shasum` on the machine where you verify artifacts.

## Verify release artifacts

Each GitHub release contains these artifacts:

| Artifact | Purpose |
| --- | --- |
| `openbao-plugin-secrets-sync_<version>_linux_amd64`, `openbao-plugin-secrets-sync_<version>_linux_arm64` | Static Linux plugin binaries. |
| `sbom-openbao-plugin-secrets-sync-linux-<arch>.spdx.json` | SPDX SBOM for each binary. |
| `go-licenses-report.csv` | Dependency license report. |
| `reproducibility-report.md` | Result of the independent rebuild comparison. |
| `checksums.txt`, `checksums.txt.bundle` | SHA-256 checksums and their keyless cosign signature bundle. |
| `provenance-index.json` | Index of assets, checksums, attestations, and the OCI image digest. |

`<version>` is the release tag without a `v` prefix, for example
`0.1.0-preview.1`. The plugin catalog version and the OCI image tag use the
`v` prefix.

1. Download the binary for your platform, `checksums.txt`,
   `checksums.txt.bundle`, and `provenance-index.json` from the release into an
   empty directory.

2. Set the release and the expected signer identity. The release workflow runs
   on `main`, so its certificate identity ends in `@refs/heads/main`:

   ```sh
   VERSION=<version>
   ARCH=amd64
   REPO=adfinis/openbao-plugin-secrets-sync
   WORKFLOW_REF=refs/heads/main
   WORKFLOW_IDENTITY="https://github.com/${REPO}/.github/workflows/release.yml@${WORKFLOW_REF}"
   ```

3. Verify the signature on `checksums.txt`:

   ```sh
   cosign verify-blob \
     --new-bundle-format=true \
     --bundle checksums.txt.bundle \
     --certificate-identity "${WORKFLOW_IDENTITY}" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

   `cosign` prints `Verified OK`. Stop if verification fails.

4. Verify the downloaded files against the signed checksums:

   ```sh
   shasum -a 256 -c --ignore-missing checksums.txt
   ```

   Each downloaded file reports `OK`.

5. Verify the build provenance attestation for the binary:

   ```sh
   gh attestation verify "./openbao-plugin-secrets-sync_${VERSION}_linux_${ARCH}" \
     --repo "${REPO}" \
     --signer-workflow "${REPO}/.github/workflows/release.yml" \
     --source-ref "${WORKFLOW_REF}" \
     --cert-oidc-issuer https://token.actions.githubusercontent.com \
     --deny-self-hosted-runners
   ```

The plugin source is licensed under Apache-2.0. Dependency licenses, including
MPL-2.0 OpenBao modules, are listed in `go-licenses-report.csv`.

## Install from a release binary

1. Copy the verified binary into the OpenBao plugin directory under the command
   name `openbao-plugin-secrets-sync`. Replace `/opt/openbao/plugins` with your
   `plugin_directory`:

   ```sh
   install -m 0755 \
     "openbao-plugin-secrets-sync_${VERSION}_linux_${ARCH}" \
     /opt/openbao/plugins/openbao-plugin-secrets-sync
   ```

2. Register the plugin with the checksum of the installed file. The value must
   match the binary's entry in the verified `checksums.txt`:

   ```sh
   sha256="$(shasum -a 256 /opt/openbao/plugins/openbao-plugin-secrets-sync | awk '{print $1}')"

   bao plugin register \
     -sha256="${sha256}" \
     -command=openbao-plugin-secrets-sync \
     -version="v${VERSION}" \
     secret openbao-plugin-secrets-sync
   ```

   OpenBao runs only a binary whose checksum matches the registered value.

3. Continue with [Mount the engine](#mount-the-engine).

## Install from the OCI image

The release also publishes `ghcr.io/adfinis/openbao-plugin-secrets-sync:v<version>`
for `linux/amd64` and `linux/arm64`. The image is an extraction artifact for
OpenBao, not a service container. It contains only the static plugin binary at
`/openbao-plugin-secrets-sync`.

1. Complete steps 1 to 4 of [Verify release artifacts](#verify-release-artifacts),
   using the binary for the platform OpenBao runs on.

2. Read the image digest from the provenance index:

   ```sh
   jq -r '.oci_plugin_image.ref, .oci_plugin_image.digest' provenance-index.json
   IMAGE_NAME=ghcr.io/adfinis/openbao-plugin-secrets-sync
   IMAGE_DIGEST=<digest>
   ```

   Replace `<digest>` with the `sha256:` digest that `jq` printed.

3. Verify the image signature and provenance by digest:

   ```sh
   cosign verify \
     --new-bundle-format=true \
     --certificate-identity "${WORKFLOW_IDENTITY}" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     "${IMAGE_NAME}@${IMAGE_DIGEST}"

   gh attestation verify "oci://${IMAGE_NAME}@${IMAGE_DIGEST}" \
     --repo "${REPO}" \
     --signer-workflow "${REPO}/.github/workflows/release.yml" \
     --source-ref "${WORKFLOW_REF}" \
     --cert-oidc-issuer https://token.actions.githubusercontent.com \
     --deny-self-hosted-runners
   ```

4. Find the checksum of the extracted binary. Use the binary's entry in the
   verified `checksums.txt`. The provenance index records the same value as
   `openbao_plugin_catalog_sha256` on the matching asset:

   ```sh
   grep "openbao-plugin-secrets-sync_${VERSION}_linux_${ARCH}\$" checksums.txt
   jq -r --arg name "openbao-plugin-secrets-sync_${VERSION}_linux_${ARCH}" \
     '.assets[] | select(.name == $name) | .openbao_plugin_catalog_sha256' \
     provenance-index.json
   ```

   Both commands must print the same checksum.

5. Add the plugin to the OpenBao server configuration. `sha256sum` is the
   checksum of the extracted binary from the previous step, not the image
   digest. OpenBao downloads the image by tag, extracts the binary, and refuses
   it unless the checksum matches:

   ```hcl
   plugin_directory         = "/opt/openbao/plugins"
   plugin_auto_download     = true
   plugin_auto_register     = true
   plugin_download_behavior = "fail"
   plugin_download_max_size = 134217728 # 128 MiB in bytes.

   plugin "secret" "openbao-plugin-secrets-sync" {
     image       = "ghcr.io/adfinis/openbao-plugin-secrets-sync"
     version     = "v<version>"
     binary_name = "openbao-plugin-secrets-sync"
     sha256sum   = "<binary-sha256>"
   }
   ```

6. Restart OpenBao so it loads the configuration. With
   `plugin_auto_register = true`, OpenBao registers the plugin in the catalog.

7. Continue with [Mount the engine](#mount-the-engine).

## Build from source

Use a source build for local evaluation only. It has no signature or
provenance evidence.

1. Build the plugin binary:

   ```sh
   make build
   ```

   The binary is written to `bin/openbao-plugin-secrets-sync` with plugin
   version `v0.0.0-dev`. `make build` targets the host platform. To build for a
   Linux OpenBao server from another platform, run
   `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build`.

2. Copy `bin/openbao-plugin-secrets-sync` into the OpenBao `plugin_directory`.

3. Register the binary:

   ```sh
   bao plugin register \
     -sha256="$(shasum -a 256 bin/openbao-plugin-secrets-sync | awk '{print $1}')" \
     -command=openbao-plugin-secrets-sync \
     -version=v0.0.0-dev \
     secret openbao-plugin-secrets-sync
   ```

4. Mount the engine with `-plugin-version=v0.0.0-dev`.

## Mount the engine

1. Enable the registered version at `secret-sync/`:

   ```sh
   bao secrets enable \
     -path=secret-sync \
     -plugin-name=openbao-plugin-secrets-sync \
     -plugin-version="v${VERSION}" \
     plugin
   ```

2. Confirm the mount responds:

   ```sh
   bao read secret-sync/config
   ```

   A new mount reports `security_posture` `standard`, `disabled` `false`, and
   `restore_guard` `false`. [Runtime settings](../configure/runtime.md#configuration-fields)
   describes every field.

Preview releases do not guarantee compatibility between versions. Read
[Preview stability](../reference/compatibility.md#preview-stability) before you
point an existing mount at a new plugin version.

Next, [sync your first secret](first-sync.md).
