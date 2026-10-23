package ci

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProvenanceIndexVerificationPrecedesUpload(t *testing.T) {
	job := loadReleaseControlWorkflow(t, "release.yml").Jobs["artifacts"]
	names := []string{
		"Generate provenance index", "Sign provenance index", "Verify provenance index signature",
		"Attest provenance index", "Verify release asset attestations", "Upload workflow artifact", "Upload release assets",
	}
	previous := -1
	for _, name := range names {
		found := -1
		for index, step := range job.Steps {
			if step.Name == name {
				found = index
			}
		}
		if found <= previous {
			t.Fatalf("step %q is missing or out of order", name)
		}
		previous = found
	}
}

func TestProvenanceIndexVerificationPolicy(t *testing.T) {
	job := loadReleaseControlWorkflow(t, "release.yml").Jobs["artifacts"]
	for _, step := range job.Steps {
		switch step.Name {
		case "Generate provenance index":
			if step.Env["RELEASE_SOURCE_REF"] != "${{ github.ref }}" {
				t.Fatal("index source ref does not match the attestation verification ref")
			}
		case "Verify provenance index signature":
			for _, required := range []string{
				"cosign verify-blob", "--bundle dist/release/provenance-index.json.bundle",
				"--certificate-identity", "--certificate-oidc-issuer", "dist/release/provenance-index.json",
			} {
				if !strings.Contains(step.Run, required) {
					t.Errorf("index signature verification lacks %q", required)
				}
			}
			if step.If != "" || step.Env["WORKFLOW_REF"] != "${{ github.workflow_ref }}" {
				t.Fatal("index signature verification must use the workflow identity and run before every upload")
			}
		case "Attest provenance index":
			if step.With["subject-path"] != "dist/release/provenance-index.json" ||
				!strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") ||
				step.If != "${{ !github.event.repository.private }}" {
				t.Fatal("index must be a separate provenance subject on public repositories")
			}
		}
	}
}

func TestReleaseAssetAttestationsRequireProvenanceIndex(t *testing.T) {
	// Reading the script also registers it as an input to the Go test cache.
	script := readTestFile(t, "verify-release-artifact-attestations.sh")
	for _, mode := range []string{"valid", "missing index", "rejected index"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"verify.sh": script, "binary": "test binary", "checksums.txt": strings.Repeat("0", 64) + "  binary\n",
				"provenance-index.json": "{}", "gh": provenanceGHStub,
			}
			for name, contents := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil { // #nosec G302 -- isolated executable fixture.
				t.Fatal(err)
			}
			if mode == "missing index" {
				if err := os.Remove(filepath.Join(dir, "provenance-index.json")); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(dir, "verify.sh")) // #nosec G204 -- isolated script fixture.
			cmd.Dir = dir
			cmd.Env = append(releaseTestEnvironment(),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE_DIR="+dir, "MODE="+mode,
				"REPO=fixture/repository", "VERSION=1.2.3", "CHECKSUMS_PATH=checksums.txt",
				"INDEX_PATH=provenance-index.json", "MAX_ATTEMPTS=1",
			)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (mode == "valid") {
				t.Fatalf("verification mode %q: err=%v output=%s", mode, err, output)
			}
			if mode != "missing index" {
				calls := readTestFile(t, filepath.Join(dir, "calls"))
				if !strings.Contains(calls, "attestation verify provenance-index.json") ||
					!strings.Contains(calls, "--source-ref refs/heads/main") {
					t.Fatalf("index was not verified against main: %s", calls)
				}
			}
		})
	}
}

const provenanceGHStub = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FIXTURE_DIR}/calls"
if [[ "$3" == provenance-index.json && "${MODE}" == 'rejected index' ]]; then
  exit 1
fi
`
