package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryVersionPolicy(t *testing.T) {
	root := copyVersionFixture(t)
	if err := checkVersionPolicy(root); err != nil {
		t.Fatal(err)
	}
}

func TestVersionPolicyThroughSymlinkedCheckout(t *testing.T) {
	root := copyVersionFixture(t)
	link := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := checkVersionPolicy(link); err != nil {
		t.Fatal(err)
	}
}

func TestVersionPolicyDetectsDrift(t *testing.T) {
	root := copyVersionFixture(t)
	p := fixturePolicy(t, root)
	tests := []struct{ name, path, before, after, want string }{
		{"Go version file", ".go-version", p.Toolchain.Go, "0.0.0", ".go-version"},
		{"Go module", "go.mod", "go " + p.Toolchain.Go, "go 0.0.0", "go.mod"},
		{"Make OpenBao", "mk/e2e.mk", p.Validation.OpenBao.Image, "wrong-image", "E2E_OPENBAO_IMAGE"},
		{"Make GitLab", "mk/e2e.mk", p.Validation.GitLab.Image, "wrong-image", "E2E_GITLAB_IMAGE"},
		{"Kind build", "test/e2e/kind/Dockerfile", p.Validation.OpenBao.Image, "wrong-image", "OPENBAO_IMAGE"},
		{"Compose image", "test/e2e/localstack/compose.yaml", p.Validation.OpenBao.Image, "wrong-image", "services.openbao"},
		{"GitLab image", "test/e2e/gitlab/compose.yaml", p.Validation.GitLab.Image, "wrong-image", "services.gitlab"},
		{
			"hardcoded image ignores override", "test/e2e/localstack/compose.yaml",
			"${E2E_OPENBAO_IMAGE:-" + p.Validation.OpenBao.Image + "}", p.Validation.OpenBao.Image, "services.openbao",
		},
		{
			"tool version", "mk/config.mk", "v" + p.Toolchain.QualityTools["gofumpt"], "v0.0.0", "GOFUMPT_VERSION",
		},
		{
			"Semgrep version", ".github/workflows/ci.yml", p.Toolchain.QualityTools["semgrep"], "0.0.0", "SEMGREP_VERSION",
		},
		{
			"Trivy version", ".github/workflows/ci.yml", "version: v" + p.Toolchain.QualityTools["trivy"],
			"version: v0.0.0", "version must equal",
		},
		{
			"floating quality tool", ".ci/versions.yaml", "gofumpt: \"" + p.Toolchain.QualityTools["gofumpt"] + "\"",
			"gofumpt: latest", "exact patch version",
		},
		{
			"workflow pin", ".github/workflows/ci.yml", p.Toolchain.GitHubActions["actions/checkout"], "v7", "policy pin",
		},
		{
			"policy digest", ".ci/versions.yaml", "imageDigest: " + p.Validation.OpenBao.ImageDigest,
			"imageDigest: sha256:" + strings.Repeat("0", 64), "matching version, image, and SHA-256 digest",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := copyVersionFixture(t)
			path := filepath.Join(fixture, tt.path)
			text, err := readText(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, tt.before) {
				t.Fatalf("fixture has no %q", tt.before)
			}
			writeVersionFixture(t, fixture, tt.path, strings.ReplaceAll(text, tt.before, tt.after))
			if err := checkVersionPolicy(fixture); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVersionPolicyDiscoversNewCallSites(t *testing.T) {
	p := fixturePolicy(t, copyVersionFixture(t))
	unknown := "example/unreviewed@" + strings.Repeat("0", 40)
	tests := []struct{ name, path, content, want string }{
		{"new workflow", ".github/workflows/new.yml", workflowProbe(unknown), "policy pin"},
		{
			"new composite action", ".github/actions/new/action.yaml",
			"runs:\n  using: composite\n  steps:\n    - uses: " + unknown + "\n", "policy pin",
		},
		{
			"new fixture", "test/e2e/new/compose.yaml",
			"services:\n  openbao-new:\n    image: wrong-image\n", "services.openbao-new",
		},
		{
			"nested fixture", "test/e2e/new/scenario/compose.yaml",
			"services:\n  openbao:\n    image: ghcr.io/openbao/openbao:0.0.0\n", "services.openbao",
		},
		{
			"renamed OpenBao service with tag", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: ghcr.io/openbao/openbao:0.0.0\n", "services.bao",
		},
		{
			"renamed OpenBao service with digest", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: ghcr.io/openbao/openbao@sha256:" + strings.Repeat("0", 64) + "\n", "services.bao",
		},
		{
			"renamed GitLab service with digest", "test/e2e/new/compose.yaml",
			"services:\n  gitlab-ce:\n    image: gitlab/gitlab-ce@sha256:" + strings.Repeat("0", 64) + "\n",
			"services.gitlab-ce",
		},
		{
			"Docker Hub GitLab reference", "test/e2e/new/compose.yaml",
			"services:\n  gitlab-ce:\n    image: docker.io/gitlab/gitlab-ce:0.0.0-ce.0\n", "services.gitlab-ce",
		},
		{
			"unqualified default image", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: ghcr.io/openbao/openbao\n", "services.bao",
		},
		{
			"renamed service with mismatched fallback", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: ${E2E_OPENBAO_IMAGE:-ghcr.io/openbao/openbao:0.0.0}\n", "services.bao",
		},
		{
			"aliased unreviewed action", ".github/workflows/new.yml",
			"env:\n  ACTION: &action " + unknown + "\n" + workflowProbe("*action"), "policy pin",
		},
		{
			"duplicate action keys", ".github/workflows/new.yml",
			workflowProbe("actions/checkout@"+p.Toolchain.GitHubActions["actions/checkout"]) +
				"        uses: " + unknown + "\n", "duplicate mapping key",
		},
		{
			"non-scalar action", ".github/workflows/new.yml", workflowProbe("{invalid: action}"), "literal action",
		},
		{
			"reviewed action alias", ".github/workflows/new.yml",
			"env:\n  ACTION: &action actions/checkout@" + p.Toolchain.GitHubActions["actions/checkout"] + "\n" +
				workflowProbe("*action"), "",
		},
		{
			"new matching fixture", "test/e2e/new/compose.yaml",
			"services:\n  openbao-new:\n    image: ${E2E_OPENBAO_IMAGE:-" + p.Validation.OpenBao.Image + "}\n", "",
		},
		{
			"nested matching fixture", "test/e2e/new/scenario/compose.yml",
			"services:\n  openbao-new:\n    image: ${E2E_OPENBAO_IMAGE:-" + p.Validation.OpenBao.Image + "}\n", "",
		},
		{
			"renamed matching OpenBao service", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: ${E2E_OPENBAO_IMAGE:-" + p.Validation.OpenBao.Image + "}\n", "",
		},
		{
			"renamed matching GitLab service", "test/e2e/new/compose.yaml",
			"services:\n  gitlab-ce:\n    image: ${E2E_GITLAB_IMAGE:-" + p.Validation.GitLab.Image + "}\n", "",
		},
		{
			"different registry", "test/e2e/new/compose.yaml",
			"services:\n  bao:\n    image: registry.example.test/openbao/openbao:0.0.0\n", "",
		},
		{
			"different OpenBao repository", "test/e2e/new/compose.yaml",
			"services:\n  helper:\n    image: ghcr.io/openbao/openbao-tools:0.0.0\n", "",
		},
		{
			"different GitLab repository", "test/e2e/new/compose.yaml",
			"services:\n  helper:\n    image: gitlab/gitlab-ce-tools:0.0.0\n", "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := copyVersionFixture(t)
			writeVersionFixture(t, root, tt.path, tt.content)
			err := checkVersionPolicy(root)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestVersionPolicyRejectsHiddenLocalActions(t *testing.T) {
	for _, ref := range []string{"./outside-action", "./.github/actions/../../outside-action", "./.github/actions/link"} {
		t.Run(ref, func(t *testing.T) {
			root := copyVersionFixture(t)
			writeVersionFixture(t, root, "outside-action/action.yml", "runs:\n  using: composite\n")
			link := filepath.Join(root, ".github/actions/link")
			if err := os.Symlink(filepath.Join(root, "outside-action"), link); err != nil {
				t.Fatal(err)
			}
			writeVersionFixture(t, root, ".github/workflows/new.yml", workflowProbe(ref))
			if err := checkVersionPolicy(root); err == nil || !strings.Contains(err.Error(), "pin discovery") {
				t.Fatalf("hidden local action accepted: %v", err)
			}
		})
	}
}

func workflowProbe(ref string) string {
	return "jobs:\n  probe:\n    runs-on: ubuntu-24.04\n    steps:\n      - uses: " + ref + "\n"
}

func fixturePolicy(t *testing.T, root string) versionPolicy {
	t.Helper()
	node, err := readYAML(filepath.Join(root, ".ci/versions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var p versionPolicy
	if err := node.Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func copyVersionFixture(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if !isVersionFixtureDirectory(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isVersionFixtureFile(rel) {
			return nil
		}
		text, err := readText(path)
		if err != nil {
			return err
		}
		writeVersionFixture(t, dest, rel, text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func isVersionFixtureDirectory(rel string) bool {
	if rel == "." {
		return true
	}
	for _, dir := range []string{".ci", ".github", "mk", "test"} {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return false
}

func isVersionFixtureFile(rel string) bool {
	if slices.Contains([]string{"go.mod", ".go-version", ".ci/versions.yaml", "test/e2e/kind/Dockerfile"}, rel) {
		return true
	}
	if strings.HasPrefix(rel, "mk/") {
		return true
	}
	if strings.HasPrefix(rel, ".github/") {
		return strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")
	}
	return strings.HasSuffix(rel, "/compose.yaml") || strings.HasSuffix(rel, "/compose.yml")
}

func writeVersionFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
