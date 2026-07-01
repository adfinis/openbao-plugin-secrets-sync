package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildIndex(t *testing.T) {
	dir := t.TempDir()
	releaseDir := filepath.Join(dir, "release")
	mustMkdir(t, releaseDir)

	binaryPath := filepath.Join(releaseDir, "openbao-plugin-secrets-sync_0.1.0_linux_amd64")
	sbomPath := filepath.Join(releaseDir, "sbom-openbao-plugin-secrets-sync-linux-amd64.spdx.json")
	reportPath := filepath.Join(releaseDir, "reproducibility-report.md")
	bundlePath := filepath.Join(releaseDir, "checksums.txt.bundle")
	mustWriteFile(t, binaryPath, "binary")
	mustWriteFile(t, sbomPath, `{"spdxVersion":"SPDX-2.3"}`)
	mustWriteFile(t, reportPath, "# Reproducibility\n")
	mustWriteFile(t, bundlePath, "bundle")

	binarySHA, _, err := fileSHA256(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	sbomSHA, _, err := fileSHA256(sbomPath)
	if err != nil {
		t.Fatal(err)
	}
	reportSHA, _, err := fileSHA256(reportPath)
	if err != nil {
		t.Fatal(err)
	}

	checksumsPath := filepath.Join(releaseDir, "checksums.txt")
	checksums := binarySHA + "  " + filepath.Base(binaryPath) + "\n" +
		sbomSHA + "  " + filepath.Base(sbomPath) + "\n" +
		reportSHA + "  " + filepath.Base(reportPath) + "\n"
	mustWriteFile(t, checksumsPath, checksums)

	idx, err := buildIndex(config{
		repo:                      "adfinis/openbao-plugin-secrets-sync",
		owner:                     "adfinis",
		version:                   "0.1.0",
		pluginVersion:             "v0.1.0",
		sourceDateEpoch:           1_704_067_200,
		binaryName:                "openbao-plugin-secrets-sync",
		ociImageRef:               "ghcr.io/adfinis/openbao-plugin-secrets-sync:v0.1.0",
		ociImageDigest:            "sha256:0123456789abcdef",
		ociImagePlatforms:         "linux/amd64,linux/arm64",
		releaseWorkflow:           "adfinis/openbao-plugin-secrets-sync/.github/workflows/release.yml",
		checksumsPath:             checksumsPath,
		checksumsBundlePath:       bundlePath,
		sbomGlob:                  filepath.Join(releaseDir, "sbom-*.spdx.json"),
		reproducibilityReportPath: reportPath,
		reproducible:              true,
		attestationsAvailable:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertEqual(t, "plugin version", idx.Release.PluginVersion, "v0.1.0")
	assertEqual(t, "generated_at_utc", idx.Release.GeneratedAtUTC, "2024-01-01T00:00:00Z")
	assertEqual(t, "assets length", len(idx.Assets), 3)
	assertEqual(t, "first asset", idx.Assets[0].Name, filepath.Base(binaryPath))
	assertEqual(t, "plugin catalog sha", idx.Assets[0].PluginCatalogSHA256, binarySHA)
	assertEqual(t, "non-binary plugin catalog sha", idx.Assets[1].PluginCatalogSHA256, "")
	assertEqual(t, "sbom length", len(idx.SBOMs), 1)
	assertTrue(t, "reproducibility should be verified", idx.Reproducible.Verified)
	assertTrue(t, "attestations should be available", idx.Attestations.Available)
	assertTrue(t, "OCI plugin image should be published", idx.OCIPluginImage.Published)
	assertEqual(t, "OCI plugin image ref", idx.OCIPluginImage.Ref, "ghcr.io/adfinis/openbao-plugin-secrets-sync:v0.1.0")
	assertEqual(t, "OCI plugin image digest", idx.OCIPluginImage.Digest, "sha256:0123456789abcdef")
	assertEqual(t, "OCI plugin image platform count", len(idx.OCIPluginImage.Platforms), 2)
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertEqual[T comparable](t *testing.T, name string, actual T, expected T) {
	t.Helper()
	if actual != expected {
		t.Fatalf("%s = %v, expected %v", name, actual, expected)
	}
}

func assertTrue(t *testing.T, msg string, value bool) {
	t.Helper()
	if !value {
		t.Fatal(msg)
	}
}
