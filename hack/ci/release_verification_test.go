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

const releaseVersion = "1.2.3-preview.1"

type signingKey struct {
	fingerprint string
	public      string
}

type releaseFixture struct {
	t       *testing.T
	dir     string
	env     []string
	trusted string
}

func TestReleaseTagVerification(t *testing.T) {
	env, keys := releaseTestKeys(t)
	tests := []struct {
		name   string
		change func(*releaseFixture)
		want   string
	}{
		{name: "valid release with a signing subkey"},
		{
			name: "later trusted main commit",
			change: func(f *releaseFixture) {
				f.trusted = f.commit("later main", "note", "later main change")
			},
		},
		{
			name: "candidate working tree cannot replace trusted key",
			change: func(f *releaseFixture) {
				f.write(".github/release-signing-keys.asc", keys[1].public)
			},
		},
		{
			name: "multiple reviewed public keys support rotation",
			change: func(f *releaseFixture) {
				f.trusted = f.commit("trust rotated key", ".github/release-signing-keys.asc", keys[0].public+keys[1].public)
				f.sign(releaseVersion, keys[1])
			},
		},
		{
			name:   "unknown key is not trusted from caller keyring",
			change: func(f *releaseFixture) { f.sign(releaseVersion, keys[1]) },
			want:   "signature does not verify",
		},
		{
			name: "candidate cannot authorize its own signing key",
			change: func(f *releaseFixture) {
				f.commit("candidate supplies key", ".github/release-signing-keys.asc", keys[1].public)
				f.sign(releaseVersion, keys[1])
				f.trusted = f.commit("restore reviewed trust", ".github/release-signing-keys.asc", keys[0].public)
			},
			want: "signature does not verify",
		},
		{
			name: "signed commit outside trusted main",
			change: func(f *releaseFixture) {
				f.commit("unmerged candidate", "note", "branch change")
				f.sign(releaseVersion, keys[0])
			},
			want: "outside trusted main history",
		},
		{
			name: "lightweight tag",
			change: func(f *releaseFixture) {
				f.git("tag", "-d", releaseVersion)
				f.git("tag", releaseVersion)
			},
			want: "must be annotated",
		},
		{
			name: "unsigned annotated tag",
			change: func(f *releaseFixture) {
				f.git("tag", "-f", "-a", releaseVersion, "-m", "unsigned")
			},
			want: "signature does not verify",
		},
		{
			name: "forged signature armor",
			change: func(f *releaseFixture) {
				body := f.git("cat-file", "tag", releaseVersion)
				start := strings.Index(body, "-----BEGIN PGP SIGNATURE-----")
				body = body[:start] + "-----BEGIN PGP SIGNATURE-----\nZm9yZ2Vk\n-----END PGP SIGNATURE-----\n"
				f.replaceTag(body)
			},
			want: "signature does not verify",
		},
		{
			name: "tampered signed message",
			change: func(f *releaseFixture) {
				body := f.git("cat-file", "tag", releaseVersion)
				f.replaceTag(strings.Replace(body, "release fixture", "tampered fixture", 1))
			},
			want: "signature does not verify",
		},
		{
			name: "valid signature under a different tag name",
			change: func(f *releaseFixture) {
				f.sign("9.9.9", keys[0])
				f.git("update-ref", "refs/tags/"+releaseVersion, f.git("rev-parse", "refs/tags/9.9.9"))
			},
			want: "signed tag name does not match",
		},
		{
			name: "nested signed tag",
			change: func(f *releaseFixture) {
				f.git("tag", "inner", "HEAD")
				f.git("tag", "-a", "-f", "inner", "-m", "inner")
				f.git("tag", "-f", "-s", "-u", keys[0].fingerprint, releaseVersion, "inner", "-m", "nested")
			},
			want: "must point directly to a commit",
		},
		{
			name: "signed blob tag",
			change: func(f *releaseFixture) {
				blob := f.git("rev-parse", "HEAD:.release-please-manifest.json")
				f.git("tag", "-f", "-s", "-u", keys[0].fingerprint, releaseVersion, blob, "-m", "blob")
			},
			want: "must point directly to a commit",
		},
		{
			name: "version mismatch",
			change: func(f *releaseFixture) {
				f.trusted = f.commit("wrong version", ".release-please-manifest.json", "{\".\":\"9.9.9\"}\n")
				f.sign(releaseVersion, keys[0])
			},
			want: "manifest version does not match",
		},
		{
			name: "missing trust file",
			change: func(f *releaseFixture) {
				f.git("rm", ".github/release-signing-keys.asc")
				f.git("commit", "-m", "remove trust file")
				f.trusted = f.git("rev-parse", "HEAD")
			},
			want: "cannot read trusted public keys",
		},
		{
			name: "empty trust file",
			change: func(f *releaseFixture) {
				f.trusted = f.commit("empty trust", ".github/release-signing-keys.asc", "")
			},
			want: "cannot import trusted public keys",
		},
		{
			name: "placeholder trust file",
			change: func(f *releaseFixture) {
				f.trusted = f.commit("placeholder", ".github/release-signing-keys.asc", "PLACEHOLDER\n")
			},
			want: "cannot import trusted public keys",
		},
		{
			name: "revoked signing key",
			change: func(f *releaseFixture) {
				home := envValue(env, "GNUPGHOME")
				cert := readTestFile(t, filepath.Join(home, "openpgp-revocs.d", keys[0].fingerprint+".rev"))
				cert = strings.Replace(cert, ":-----BEGIN PGP PUBLIC KEY BLOCK-----", "-----BEGIN PGP PUBLIC KEY BLOCK-----", 1)
				f.trusted = f.commit("revoke key", ".github/release-signing-keys.asc", keys[0].public+cert)
			},
			want: "signature",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReleaseFixture(t, env, keys)
			if tt.change != nil {
				tt.change(f)
			}
			f.verify("", tt.want)
		})
	}
	t.Run("dispatch binds expected commit", func(t *testing.T) {
		f := newReleaseFixture(t, env, keys)
		f.verify(f.trusted, "")
		other := f.commit("later main", "note", "later main change")
		f.trusted = other
		f.verify(other, "does not match dispatch commit")
		f.verify("HEAD", "expected commit must be a full commit ID")
	})
}

func TestExportReleasePublicKey(t *testing.T) {
	env, keys := releaseTestKeys(t)
	private := runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--armor",
		"--export-secret-keys", keys[0].fingerprint)
	outDir := filepath.Join(t.TempDir(), "public")
	script, err := filepath.Abs("export-release-public-key.sh")
	if err != nil {
		t.Fatal(err)
	}
	exportEnv := append(env, "PUBLIC_KEY_DIR="+outDir, "OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY="+private)
	runReleaseCommand(t, "", exportEnv, "", "bash", script)
	public := readTestFile(t, filepath.Join(outDir, "release-signing-keys.asc"))
	if public != keys[0].public || strings.Contains(public, "PRIVATE KEY") {
		t.Fatal("export did not contain only the expected public key")
	}
	if got := strings.TrimSpace(readTestFile(t, filepath.Join(outDir, "fingerprint.txt"))); got != keys[0].fingerprint {
		t.Fatalf("public fingerprint = %s, want %s", got, keys[0].fingerprint)
	}
	private += "\n" + runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--armor",
		"--export-secret-keys", keys[1].fingerprint)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- script is an absolute path to the checked-in helper under test.
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Env = append(env, "PUBLIC_KEY_DIR="+filepath.Join(t.TempDir(), "rejected"),
		"OPENBAO_SECRET_SYNC_RELEASE_TAG_GPG_PRIVATE_KEY="+private)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "exactly one primary secret key") {
		t.Fatal("export must reject an ambiguous signing secret")
	}
}

func releaseTestKeys(t *testing.T) ([]string, []signingKey) {
	t.Helper()
	for _, tool := range []string{"git", "gpg", "gpgconf", "jq", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("release verification tests require %s: %v", tool, err)
		}
	}
	// Keep GnuPG socket paths short on macOS, independently of the Go test directory.
	home, err := os.MkdirTemp("/tmp", "release-test-gpg-")
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GIT_") && !strings.HasPrefix(name, "GITHUB_") &&
			name != "GNUPGHOME" && name != "TMPDIR" {
			env = append(env, entry)
		}
	}
	env = append(env, "GNUPGHOME="+home, "TMPDIR=/tmp", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	t.Cleanup(func() {
		// #nosec G204 -- home is the temporary keyring created by this test.
		cmd := exec.Command("gpgconf", "--homedir", home, "--kill", "all")
		_ = cmd.Run()
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	keys := make([]signingKey, 0, 2)
	identities := []string{"Release Fixture <release@example.invalid>", "Other Fixture <other@example.invalid>"}
	for _, identity := range identities {
		runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--pinentry-mode", "loopback",
			"--passphrase", "", "--quick-generate-key", identity, "ed25519", "sign", "0")
		key := readSigningKey(t, env, identity)
		if len(keys) == 0 {
			runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--pinentry-mode", "loopback",
				"--passphrase", "", "--quick-add-key", key.fingerprint, "ed25519", "sign", "0")
			key = readSigningKey(t, env, identity)
		}
		keys = append(keys, key)
	}
	return env, keys
}

func readSigningKey(t *testing.T, env []string, identity string) signingKey {
	t.Helper()
	listing := runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--with-colons",
		"--list-keys", identity)
	for line := range strings.SplitSeq(listing, "\n") {
		if strings.HasPrefix(line, "fpr:") {
			fingerprint := strings.Split(line, ":")[9]
			public := runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--armor", "--export", fingerprint)
			return signingKey{fingerprint: fingerprint, public: public + "\n"}
		}
	}
	t.Fatal("test key has no fingerprint")
	return signingKey{}
}

func TestExpiredReleaseSigningKey(t *testing.T) {
	env, keys := releaseTestKeys(t)
	identity := "Expired Fixture <expired@example.invalid>"
	oldTime := "1577836800" // 2020-01-01 UTC, before this repository existed.
	runReleaseCommand(t, "", env, "", "gpg", "--no-options", "--batch", "--pinentry-mode", "loopback",
		"--passphrase", "", "--faked-system-time", oldTime, "--quick-generate-key", identity, "ed25519", "sign", "1d")
	expired := readSigningKey(t, env, identity)
	f := newReleaseFixture(t, env, keys)
	f.trusted = f.commit("expired trust", ".github/release-signing-keys.asc", expired.public)
	f.write("gpg-sign", "#!/usr/bin/env bash\n"+
		"exec gpg --no-options --batch --pinentry-mode loopback --passphrase '' "+
		"--faked-system-time "+oldTime+" \"$@\"\n")
	f.sign(releaseVersion, expired)
	f.verify("", "signature")
}

func newReleaseFixture(t *testing.T, env []string, keys []signingKey) *releaseFixture {
	t.Helper()
	f := &releaseFixture{t: t, dir: t.TempDir(), env: env}
	f.git("init", "-b", "main")
	f.git("config", "user.name", "Release Fixture")
	f.git("config", "user.email", "release@example.invalid")
	f.git("config", "commit.gpgsign", "false")
	f.git("config", "tag.gpgsign", "false")
	f.git("config", "gpg.format", "openpgp")
	f.write("gpg-sign", "#!/usr/bin/env bash\n"+
		"exec gpg --no-options --batch --pinentry-mode loopback --passphrase '' \"$@\"\n")
	// #nosec G302 -- the temporary signing wrapper must be executable by the test user.
	if err := os.Chmod(filepath.Join(f.dir, "gpg-sign"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.git("config", "gpg.openpgp.program", filepath.Join(f.dir, "gpg-sign"))
	f.write(".github/release-signing-keys.asc", keys[0].public)
	f.trusted = f.commit("release fixture", ".release-please-manifest.json", "{\".\":\""+releaseVersion+"\"}\n")
	f.sign(releaseVersion, keys[0])
	return f
}

func (f *releaseFixture) write(path, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *releaseFixture) git(args ...string) string {
	f.t.Helper()
	return runReleaseCommand(f.t, f.dir, f.env, "", "git", args...)
}

func (f *releaseFixture) commit(message, path, content string) string {
	f.t.Helper()
	f.write(path, content)
	f.git("add", ".")
	f.git("commit", "-m", message)
	return f.git("rev-parse", "HEAD")
}

func (f *releaseFixture) sign(version string, key signingKey) {
	f.t.Helper()
	f.git("tag", "-f", "-s", "-u", key.fingerprint, version, "-m", "release fixture")
}

func (f *releaseFixture) replaceTag(body string) {
	f.t.Helper()
	object := runReleaseCommand(f.t, f.dir, f.env, body, "git", "hash-object", "-t", "tag", "-w", "--stdin")
	f.git("update-ref", "refs/tags/"+releaseVersion, object)
}

func (f *releaseFixture) verify(expected, want string) {
	f.t.Helper()
	script, err := filepath.Abs("verify-release-tag.sh")
	if err != nil {
		f.t.Fatal(err)
	}
	outputPath := filepath.Join(f.t.TempDir(), "output")
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- script is an absolute path to the checked-in helper under test.
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = f.dir
	cmd.Env = append(f.env, "VERSION="+releaseVersion, "TRUSTED_COMMIT="+f.trusted,
		"EXPECTED_COMMIT="+expected, "GITHUB_OUTPUT="+outputPath)
	output, err := cmd.CombinedOutput()
	if want != "" {
		if err == nil || !strings.Contains(string(output), want) {
			f.t.Fatalf("verification error = %v, want %q; output: %s", err, want, output)
		}
		// #nosec G304 -- outputPath is inside this test's temporary directory.
		if data, _ := os.ReadFile(outputPath); len(data) != 0 {
			f.t.Fatal("failed verification emitted build outputs")
		}
		return
	}
	if err != nil {
		f.t.Fatalf("valid release rejected: %v; output: %s", err, output)
	}
	outputs := readTestFile(f.t, outputPath)
	commit := f.git("rev-parse", "refs/tags/"+releaseVersion+"^{commit}")
	object := f.git("rev-parse", "refs/tags/"+releaseVersion)
	if outputs != "commit="+commit+"\ntag_object="+object+"\n" {
		f.t.Fatalf("unexpected verified build outputs: %q", outputs)
	}
}

func runReleaseCommand(t *testing.T, dir string, env []string, input, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- commands and arguments come from the test fixtures, with an isolated environment.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(input)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v failed: %v; stderr: %s", name, args, err, stderr.String())
	}
	return strings.TrimSpace(string(output))
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	// #nosec G304 -- callers read only files in isolated test repositories and keyrings.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func envValue(env []string, name string) string {
	for _, entry := range env {
		if value, found := strings.CutPrefix(entry, name+"="); found {
			return value
		}
	}
	return ""
}
