package backend

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type goldenFixture struct {
	file          string
	updateEnv     string
	updateCommand string
	description   string
}

func assertGoldenFixture(t *testing.T, fixture goldenFixture, actual []byte) {
	t.Helper()

	path := filepath.FromSlash(fixture.file)
	if os.Getenv(fixture.updateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(path, actual, 0o600); err != nil {
			t.Fatalf("write golden file: %v", err)
		}
		return
	}

	expected, err := os.ReadFile(path) //nolint:gosec // Test fixture paths are fixed by package constants.
	if err != nil {
		t.Fatalf("read golden file: %v; run %s", err, fixture.updateCommand)
	}
	if !bytes.Equal(bytes.TrimSpace(expected), bytes.TrimSpace(actual)) {
		t.Fatalf(
			"%s changed; review the diff and run %s if intentional\n%s",
			fixture.description,
			fixture.updateCommand,
			goldenMismatch(expected, actual),
		)
	}
}

func goldenMismatch(expected []byte, actual []byte) string {
	return fmt.Sprintf("--- expected\n%s\n--- actual\n%s", expected, actual)
}
