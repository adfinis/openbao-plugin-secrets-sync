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

type securityGateFixture struct {
	makefile string
	tools    map[string]string
	log      string
}

func TestRequiredSecurityGates(t *testing.T) {
	targets := []struct {
		name  string
		tools []string
	}{
		{name: "vulncheck", tools: []string{"govulncheck"}},
		{name: "security-scan-fs", tools: []string{"trivy"}},
		{name: "security-scan-image", tools: []string{"trivy"}},
		{name: "security-ci", tools: []string{"govulncheck", "trivy"}},
		{name: "ci-core", tools: []string{"govulncheck", "trivy"}},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			for _, tool := range target.tools {
				for _, failure := range []string{"missing", "not executable", "scanner failure"} {
					t.Run(tool+"/"+failure, func(t *testing.T) {
						f := newSecurityGateFixture(t)
						failTool := f.breakTool(t, tool, failure)
						output, err := f.run(t, target.name, failTool)
						if err == nil {
							t.Fatalf("%s succeeded with %s %s; output: %s", target.name, tool, failure, output)
						}
						if !strings.Contains(string(output), tool) {
							t.Fatalf("failure does not identify %s: %s", tool, output)
						}
					})
				}
			}
			t.Run("successful scanners", func(t *testing.T) {
				f := newSecurityGateFixture(t)
				if output, err := f.run(t, target.name, ""); err != nil {
					t.Fatalf("successful scanners rejected: %v; output: %s", err, output)
				}
				log := readTestFile(t, f.log)
				for _, tool := range target.tools {
					if !strings.Contains(log, tool+":") {
						t.Fatalf("%s did not invoke required %s: %s", target.name, tool, log)
					}
				}
			})
		})
	}
}

func newSecurityGateFixture(t *testing.T) securityGateFixture {
	t.Helper()
	dir := t.TempDir()
	f := securityGateFixture{
		makefile: filepath.Join(dir, "Makefile"),
		tools:    make(map[string]string, 3),
		log:      filepath.Join(dir, "invocations"),
	}
	for _, name := range []string{"govulncheck", "trivy", "go-licenses"} {
		// Exercise tool overrides whose paths contain spaces.
		path := filepath.Join(dir, "scanner tools", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		script := `#!/bin/sh
tool="${0##*/}"
printf '%s:%s\n' "$tool" "$*" >> "$TEST_TOOL_LOG"
if [ "$tool" = "$TEST_FAIL_TOOL" ]; then
  printf '%s reported a scanner failure\n' "$tool" >&2
  exit 42
fi
`
		// #nosec G306 -- the fake scanner must be executable by the test user.
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		f.tools[name] = path
	}
	var makefile strings.Builder
	for _, name := range []string{"security.mk", "ci.mk"} {
		path, err := filepath.Abs(filepath.Join("..", "..", "mk", name))
		if err != nil {
			t.Fatal(err)
		}
		makefile.WriteString("include " + strings.ReplaceAll(path, " ", "\\ ") + "\n")
	}
	// Use the real security recipes and ci-core dependency graph. Unrelated
	// targets do no work, so the regression needs no network or real scanner.
	makefile.WriteString("verify-tidy lint test test-race fuzz build release-artifacts:\n\t@:\n")
	if err := os.WriteFile(f.makefile, []byte(makefile.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f securityGateFixture) breakTool(t *testing.T, tool, failure string) string {
	t.Helper()
	switch failure {
	case "missing":
		f.tools[tool] += "-absent"
	case "not executable":
		if err := os.Chmod(f.tools[tool], 0o600); err != nil {
			t.Fatal(err)
		}
	case "scanner failure":
		return tool
	}
	return ""
}

func (f securityGateFixture) run(t *testing.T, target, failTool string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	// #nosec G204 -- Make targets and scanner paths are controlled by this test.
	cmd := exec.CommandContext(ctx, "make", "--no-print-directory", "-j1", "-f", f.makefile, target,
		"GOVULNCHECK="+f.tools["govulncheck"], "TRIVY="+f.tools["trivy"], "GO_LICENSES="+f.tools["go-licenses"])
	cmd.Dir = filepath.Dir(f.makefile)
	cmd.Env = append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "TEST_TOOL_LOG="+f.log, "TEST_FAIL_TOOL="+failTool)
	return cmd.CombinedOutput()
}
