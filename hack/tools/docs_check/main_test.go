package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pageA = "docs/a.md"

func TestRepositoryDocs(t *testing.T) {
	if err := checkDocs("../../.."); err != nil {
		t.Fatal(err)
	}
}

func TestDocsCheckDetectsDrift(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "missing verifiedBy",
			files: map[string]string{pageA: "# A\n"},
			want:  "frontmatter must list verifiedBy",
		},
		{
			name:  "missing evidence path",
			files: map[string]string{pageA: docPage("internal/missing.go", "# A\n")},
			want:  `verifiedBy path "internal/missing.go" does not exist`,
		},
		{
			name:  "unclean evidence path",
			files: map[string]string{pageA: docPage("./go.mod", "# A\n")},
			want:  "must be repository-relative and clean",
		},
		{
			name:  "unknown frontmatter field",
			files: map[string]string{pageA: "---\ntitle: A\nverifiedBy:\n  - go.mod\n---\n# A\n"},
			want:  "field title not found",
		},
		{
			name:  "broken link",
			files: map[string]string{pageA: docPage("go.mod", "# A\n\nSee [b](b.md).\n")},
			want:  `docs/a.md:7: link target "b.md" does not exist`,
		},
		{
			name: "missing anchor",
			files: map[string]string{
				pageA:       docPage("go.mod", "# A\n\nSee [b](b.md#nope).\n"),
				"docs/b.md": docPage("go.mod", "# B\n\n## Yes\n"),
			},
			want: `anchor "nope" not found in docs/b.md`,
		},
		{
			name: "page missing from index",
			files: map[string]string{
				"docs/README.md": "# Docs\n",
				pageA:            docPage("go.mod", "# A\n"),
			},
			want: "docs/a.md: page is not linked from docs/README.md",
		},
		{
			name:  "link from a repository page",
			files: map[string]string{rootReadme: "See [gone](docs/gone.md).\n"},
			want:  `README.md:1: link target "docs/gone.md" does not exist`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := docsFixture(t, tt.files)
			err := checkDocs(root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("checkDocs() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDocsCheckAcceptsValidDocs(t *testing.T) {
	root := docsFixture(t, map[string]string{
		rootReadme: "Start with the [docs](docs/README.md).\n",
		pageA: docPage("internal", strings.Join([]string{
			"# A",
			"",
			"## Use `secret-path` shapes",
			"",
			"## Repeat",
			"",
			"## Repeat",
			"",
			"Links: [self](#use-secret-path-shapes), [second](#repeat-1), [b](b.md#mutation-gates),",
			"[external](https://example.com/x.md), and [dir](../internal).",
			"",
			"```text",
			"[ignored](missing.md)",
			"## Ignored heading",
			"```",
			"",
			"Inline `[ignored](missing.md)` code.",
			"",
		}, "\n")),
		"docs/b.md": docPage("go.mod", "# B\n\n## Mutation gates\n"),
	})
	if err := checkDocs(root); err != nil {
		t.Fatal(err)
	}
}

func TestHeadingSlug(t *testing.T) {
	tests := map[string]string{
		"Mutation gates":                         "mutation-gates",
		"Queue, status, and reconcile":           "queue-status-and-reconcile",
		"Use `secret-path` shapes":               "use-secret-path-shapes",
		"Inspect the OpenAPI document":           "inspect-the-openapi-document",
		"GitLab and Kubernetes endpoint policy":  "gitlab-and-kubernetes-endpoint-policy",
		"Confused-deputy controls":               "confused-deputy-controls",
		"Error classes and retry (`rate_limit`)": "error-classes-and-retry-rate_limit",
	}
	for heading, want := range tests {
		if got := headingSlug(heading); got != want {
			t.Errorf("headingSlug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func docPage(evidence, body string) string {
	return "---\nverifiedBy:\n  - " + evidence + "\n---\n" + body
}

// docsFixture writes the files plus an index linking every docs page unless the test supplies its own index.
func docsFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example\n")
	write("internal/x.go", "package internal\n")
	if _, ok := files[docsIndex]; !ok {
		var index strings.Builder
		index.WriteString("# Docs\n\n")
		for rel := range files {
			if strings.HasPrefix(rel, "docs/") {
				index.WriteString("- [" + rel + "](" + strings.TrimPrefix(rel, "docs/") + ")\n")
			}
		}
		write(docsIndex, index.String())
	}
	for rel, text := range files {
		write(rel, text)
	}
	return root
}
