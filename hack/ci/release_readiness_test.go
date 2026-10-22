package ci

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	readinessHead          = "1111111111111111111111111111111111111111"
	changedAfterPreflight  = "changed after preflight"
	readinessFeatureBranch = "feature"
)

type readinessFixture struct {
	dir     string
	pr      map[string]interface{}
	reviews []map[string]interface{}
}

func TestReleaseReadinessPolicy(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *readinessFixture)
		env    []string
		want   string
	}{
		{name: "current head approval"},
		{
			name: "ordinary PR needs no release approver", env: []string{"REQUIRED_APPROVER="},
			change: func(t *testing.T, f *readinessFixture) { f.ordinary(t, false) },
		},
		{name: "ordinary fork PR", change: func(t *testing.T, f *readinessFixture) { f.ordinary(t, true) }},
		{name: "missing approver", env: []string{"REQUIRED_APPROVER="}, want: "not configured"},
		{name: "missing label", change: func(t *testing.T, f *readinessFixture) {
			f.writeJSON(t, "labels.json", [][]map[string]string{{}})
		}, want: "label is missing"},
		{name: "label APIs disagree", change: func(t *testing.T, f *readinessFixture) {
			f.pr["labels"] = []map[string]string{}
		}, want: "label is missing"},
		{name: "old head approval", change: func(t *testing.T, f *readinessFixture) {
			f.reviews[0]["commit_id"] = strings.Repeat("2", 40)
		}, want: "current-head approval"},
		{name: "changes requested revokes approval", change: func(t *testing.T, f *readinessFixture) {
			f.addReview("CHANGES_REQUESTED")
		}, want: "revoked"},
		{name: "dismissal revokes approval", change: func(t *testing.T, f *readinessFixture) {
			f.addReview("DISMISSED")
		}, want: "revoked"},
		{name: "comments preserve approval", change: func(t *testing.T, f *readinessFixture) {
			f.addReview("COMMENTED")
		}},
		{name: "draft release", change: func(t *testing.T, f *readinessFixture) {
			f.pr["draft"] = true
		}, want: "must not be draft"},
		{name: "fork release", change: func(t *testing.T, f *readinessFixture) {
			f.pr["head"].(map[string]interface{})["repo"] = map[string]string{"full_name": "fork/repository"}
		}, want: "same-repository"},
		{name: "manifest cannot bypass release branch", change: func(t *testing.T, f *readinessFixture) {
			f.pr["head"].(map[string]interface{})["ref"] = readinessFeatureBranch
		}, want: "release branch"},
		{name: "incomplete release metadata", change: func(t *testing.T, f *readinessFixture) {
			f.pr["changed_files"] = 1
			f.writeJSON(t, "files.json", [][]map[string]string{{{"filename": ".release-please-manifest.json"}}})
		}, want: "changelog and manifest"},
		{name: "deleted release metadata", change: func(t *testing.T, f *readinessFixture) {
			f.writeJSON(t, "files.json", [][]map[string]string{{
				{"filename": "CHANGELOG.md"}, {"filename": ".release-please-manifest.json", "status": "removed"},
			}})
		}, want: "changelog and manifest"},
		{name: "truncated files cannot bypass classification", change: func(t *testing.T, f *readinessFixture) {
			f.pr["changed_files"] = 3001
		}, want: "file list is incomplete"},
		{name: "renamed manifest remains release metadata", change: func(t *testing.T, f *readinessFixture) {
			f.pr["head"].(map[string]interface{})["ref"] = readinessFeatureBranch
			f.pr["changed_files"] = 1
			f.writeJSON(t, "files.json", [][]map[string]string{{{
				"filename": "renamed.json", "previous_filename": ".release-please-manifest.json",
			}}})
		}, want: "release branch"},
		{name: "large patch and review bodies", change: func(t *testing.T, f *readinessFixture) {
			f.pr["body"] = strings.Repeat("large PR body ", 30000)
			f.reviews[0]["body"] = strings.Repeat("large review body ", 30000)
			f.writeJSON(t, "files.json", [][]map[string]string{{
				{"filename": "CHANGELOG.md", "patch": strings.Repeat("large patch ", 30000)},
				{"filename": ".release-please-manifest.json"},
			}})
		}},
		{name: "API failure", env: []string{"FAIL_API=reviews"}, want: "fixture API failure"},
		{
			name: "new head at initial fetch", env: []string{"EXPECTED_HEAD_SHA=" + strings.Repeat("2", 40)},
			want: "head or base changed",
		},
		{name: "new head during evaluation", change: func(t *testing.T, f *readinessFixture) {
			f.pr["head"].(map[string]interface{})["sha"] = strings.Repeat("2", 40)
			f.writeJSON(t, "pr-final.json", f.pr)
			f.pr["head"].(map[string]interface{})["sha"] = readinessHead
		}, want: "changed during"},
		{name: "label removed during evaluation", change: func(t *testing.T, f *readinessFixture) {
			f.pr["labels"] = []map[string]string{}
			f.writeJSON(t, "pr-final.json", f.pr)
			f.pr["labels"] = []map[string]string{{"name": "release:ready"}}
		}, want: "changed during"},
		{name: "review dismissed during evaluation", change: func(t *testing.T, f *readinessFixture) {
			f.writeJSON(t, "reviews-final.json", [][]map[string]interface{}{{{"id": 1, "state": "DISMISSED"}}})
		}, want: "Reviews changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReadinessFixture(t)
			if tt.change != nil {
				tt.change(t, f)
			}
			f.sync(t)
			f.check(t, "release-readiness.sh", tt.want, tt.env...)
		})
	}
}

func TestReadinessStatusTargetsExpectedHead(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "revoked"}[revoked], func(t *testing.T) {
			f := newReadinessFixture(t)
			want := ""
			if revoked {
				f.addReview("DISMISSED")
				want = "revoked"
			}
			f.sync(t)
			f.check(t, "publish-release-readiness.sh", want)
			log := readTestFile(t, filepath.Join(f.dir, "calls"))
			pending := "/statuses/" + readinessHead + " --method POST -f context=Release Readiness -f state=pending"
			if !strings.Contains(log, pending) {
				t.Fatalf("status is not pending on expected head: %s", log)
			}
			state := "success"
			if revoked {
				state = "failure"
			}
			if !strings.Contains(log, "-f state="+state) || (revoked && strings.Contains(log, "-f state=success")) {
				t.Fatalf("incorrect terminal status: %s", log)
			}
		})
	}
}

func TestReleaseTagReadOnlyPreflight(t *testing.T) {
	scenarios := []string{
		"valid", "approval after merge", "approval at merge",
		"wrong approved head", "wrong approved merge", "wrong version",
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			f := newReadinessFixture(t)
			gitEnv := releaseTestEnvironment()
			runReleaseCommand(t, f.dir, gitEnv, "", "git", "init", "-b", "main")
			f.writeJSON(t, ".release-please-manifest.json", map[string]string{".": releaseVersion})
			f.writeJSON(t, "CHANGELOG.md", "release notes")
			runReleaseCommand(t, f.dir, gitEnv, "", "git", "add", ".release-please-manifest.json", "CHANGELOG.md")
			runReleaseCommand(t, f.dir, gitEnv, "", "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
				"-c", "commit.gpgsign=false", "commit", "-m", "fixture")
			merge := runReleaseCommand(t, f.dir, gitEnv, "", "git", "rev-parse", "HEAD")
			f.pr["state"], f.pr["merged"] = "closed", true
			f.pr["merged_at"], f.pr["merge_commit_sha"] = "2026-10-22T12:00:00Z", merge
			f.writeJSON(t, "pr-view.json", map[string]interface{}{
				"state": "MERGED", "mergeCommit": map[string]string{"oid": merge}, "headRefOid": readinessHead,
			})
			f.writeJSON(t, "pr-list.json", []map[string]interface{}{
				{"number": 6, "title": "chore(main): release " + releaseVersion},
			})
			env := []string{
				"PREFLIGHT_ONLY=1", "GH_READ_TOKEN=fixture", "GH_WRITE_TOKEN=", "APPROVED_HEAD_SHA=" + readinessHead,
				"APPROVED_MERGE_SHA=" + merge, "APPROVED_PR_NUMBER=6", "APPROVED_VERSION=" + releaseVersion,
			}
			want := ""
			switch scenario {
			case "approval after merge":
				f.reviews[0]["submitted_at"] = "2026-10-22T13:00:00Z"
				want = "predate merge"
			case "approval at merge":
				f.reviews[0]["submitted_at"] = f.pr["merged_at"]
				want = "predate merge"
			case "wrong approved head":
				env = append(env, "APPROVED_HEAD_SHA="+strings.Repeat("2", 40))
				want = changedAfterPreflight
			case "wrong approved merge":
				env = append(env, "APPROVED_MERGE_SHA="+strings.Repeat("2", 40))
				want = changedAfterPreflight
			case "wrong version":
				env = append(env, "APPROVED_VERSION=9.9.9")
				want = changedAfterPreflight
			}
			f.sync(t)
			f.check(t, "create-release-tag-and-draft.sh", want, env...)
			calls := readTestFile(t, filepath.Join(f.dir, "calls"))
			if strings.Contains(calls, "--method POST") || strings.Contains(calls, "release create") {
				t.Fatalf("preflight attempted GitHub mutation: %s", calls)
			}
			if got := runReleaseCommand(t, f.dir, gitEnv, "", "git", "tag", "--list"); got != "" {
				t.Fatalf("preflight created tag: %s", got)
			}
			if want == "" {
				outputs := readTestFile(t, filepath.Join(f.dir, "outputs"))
				for _, required := range []string{"candidate_ready=true", "head_sha=" + readinessHead, "merge_sha=" + merge} {
					if !strings.Contains(outputs, required) {
						t.Fatalf("preflight missing %q: %s", required, outputs)
					}
				}
			}
		})
	}
}

func TestReleaseReadinessIsolatesHookRepository(t *testing.T) {
	outer := t.TempDir()
	outerEnv := releaseTestEnvironment()
	runReleaseCommand(t, outer, outerEnv, "", "git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(outer, "sentinel"), []byte("outer worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReleaseCommand(t, outer, outerEnv, "", "git", "add", "sentinel")
	runReleaseCommand(t, outer, outerEnv, "", "git", "-c", "user.name=Outer Fixture",
		"-c", "user.email=outer@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "outer fixture")
	gitDir := filepath.Join(outer, ".git")
	before := map[string]string{}
	for _, path := range []string{"config", "index", "HEAD", "refs/heads/main"} {
		before[path] = readTestFile(t, filepath.Join(gitDir, path))
	}
	for name, value := range map[string]string{
		"GIT_DIR": gitDir, "GIT_COMMON_DIR": gitDir, "GIT_WORK_TREE": outer,
		"GIT_INDEX_FILE": filepath.Join(gitDir, "index"), "GIT_OBJECT_DIRECTORY": filepath.Join(gitDir, "objects"),
		"GIT_CONFIG": filepath.Join(gitDir, "config"), "GIT_CONFIG_GLOBAL": filepath.Join(gitDir, "config"),
		"GIT_CONFIG_SYSTEM": filepath.Join(gitDir, "config"), "GIT_CONFIG_COUNT": "1",
		"GIT_CONFIG_KEY_0": "core.bare", "GIT_CONFIG_VALUE_0": "true",
		"GIT_CONFIG_PARAMETERS": "'core.bare=true'", "GIT_PREFIX": "hook/",
	} {
		t.Setenv(name, value)
	}
	// Both repository setup and the Bash preflight must ignore the caller's repository.
	t.Run("preflight under hook context", TestReleaseTagReadOnlyPreflight)
	for path, contents := range before {
		if got := readTestFile(t, filepath.Join(gitDir, path)); got != contents {
			t.Errorf("fixture changed outer repository %s", path)
		}
	}
	if got := readTestFile(t, filepath.Join(outer, "sentinel")); got != "outer worktree\n" {
		t.Error("fixture changed the outer worktree")
	}
}

func newReadinessFixture(t *testing.T) *readinessFixture {
	t.Helper()
	f := &readinessFixture{dir: t.TempDir()}
	f.pr = map[string]interface{}{
		"number": 6, "state": "open", "draft": false, "merged": false, "changed_files": 2,
		"head": map[string]interface{}{
			"sha": readinessHead, "ref": "release-please--branches--main",
			"repo": map[string]string{"full_name": "fixture/repository"},
		},
		"base": map[string]string{"ref": "main"}, "labels": []map[string]string{{"name": "release:ready"}},
	}
	f.reviews = []map[string]interface{}{{
		"id": 1, "state": "APPROVED", "commit_id": readinessHead,
		"submitted_at": "2026-10-22T10:00:00Z", "user": map[string]string{"login": "dc-tec"},
	}}
	// Separate pages ensure the production helper asks gh to paginate and slurp.
	f.writeJSON(t, "files.json", [][]map[string]string{
		{{"filename": "CHANGELOG.md"}}, {{"filename": ".release-please-manifest.json"}},
	})
	f.writeJSON(t, "labels.json", [][]map[string]string{{}, {{"name": "release:ready"}}})
	// #nosec G306 -- executable fixture used instead of real GitHub access.
	if err := os.WriteFile(filepath.Join(f.dir, "gh"), []byte(readinessGHStub), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *readinessFixture) ordinary(t *testing.T, fork bool) {
	t.Helper()
	f.pr["head"].(map[string]interface{})["ref"] = readinessFeatureBranch
	f.pr["changed_files"] = 1
	if fork {
		f.pr["head"].(map[string]interface{})["repo"] = map[string]string{"full_name": "fork/repository"}
	}
	f.writeJSON(t, "files.json", [][]map[string]string{{{"filename": "README.md"}}})
}

func (f *readinessFixture) addReview(state string) {
	f.reviews = append(f.reviews, map[string]interface{}{
		"id": 2, "state": state, "commit_id": readinessHead,
		"submitted_at": "2026-10-22T11:00:00Z", "user": map[string]string{"login": "dc-tec"},
	})
}

func (f *readinessFixture) sync(t *testing.T) {
	t.Helper()
	f.writeJSON(t, "pr.json", f.pr)
	f.writeJSON(t, "reviews.json", [][]map[string]interface{}{{}, f.reviews})
}

func (f *readinessFixture) writeJSON(t *testing.T, name string, data interface{}) {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, name), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *readinessFixture) check(t *testing.T, script, want string, extra ...string) {
	t.Helper()
	// Register shell sources as Go test cache inputs as well as executing them.
	sources := []string{"release-readiness.sh", "publish-release-readiness.sh", "create-release-tag-and-draft.sh"}
	for _, source := range sources {
		_ = readTestFile(t, source)
	}
	path, err := filepath.Abs(script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", path) // #nosec G204 -- checked-in helper and isolated executable fixtures.
	cmd.Dir = f.dir
	cmd.Env = append(releaseTestEnvironment(),
		"PATH="+f.dir+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE_DIR="+f.dir,
		"REPO=fixture/repository", "PR_NUMBER=6", "EXPECTED_HEAD_SHA="+readinessHead, "READINESS_MODE=merge",
		"REQUIRED_APPROVER=dc-tec", "GITHUB_RUN_ID=123", "GITHUB_OUTPUT="+filepath.Join(f.dir, "outputs"))
	cmd.Env = append(cmd.Env, extra...)
	output, err := cmd.CombinedOutput()
	if want != "" {
		if err == nil || !strings.Contains(string(output), want) {
			t.Fatalf("error = %v, want %q; output: %s", err, want, output)
		}
	} else if err != nil {
		t.Fatalf("valid candidate rejected: %v; output: %s", err, output)
	}
}

const readinessGHStub = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FIXTURE_DIR}/calls"
if [[ "$1" == pr ]]; then
  cat "${FIXTURE_DIR}/pr-$2.json"
  exit 0
fi
[[ "$1" == api ]] || exit 95
endpoint="$2"
if [[ -n "${FAIL_API:-}" && "$endpoint" == *"/${FAIL_API}" ]]; then
  echo 'fixture API failure' >&2
  exit 42
fi
case "$endpoint" in
  */statuses/*) exit 0 ;;
  */files|*/reviews|*/labels|*/pulls)
    [[ "$*" == *'--paginate --slurp'* ]] || exit 94
    name="${endpoint##*/}"
    path="${FIXTURE_DIR}/${name}.json"
    if [[ -e "${FIXTURE_DIR}/${name}-seen" && -f "${FIXTURE_DIR}/${name}-final.json" ]]; then
      path="${FIXTURE_DIR}/${name}-final.json"
    fi
    touch "${FIXTURE_DIR}/${name}-seen"
    cat "$path"
    ;;
  */pulls/6)
    path="${FIXTURE_DIR}/pr.json"
    if [[ -e "${FIXTURE_DIR}/pr-seen" && -f "${FIXTURE_DIR}/pr-final.json" ]]; then
      path="${FIXTURE_DIR}/pr-final.json"
    fi
    touch "${FIXTURE_DIR}/pr-seen"
    cat "$path"
    ;;
  *) echo "unexpected fixture command: $*" >&2; exit 93 ;;
esac
`
