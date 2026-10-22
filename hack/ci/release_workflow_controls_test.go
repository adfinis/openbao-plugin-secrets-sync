package ci

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type releaseControlWorkflow struct {
	On struct {
		Target struct {
			Paths []string `yaml:"paths"`
			Types []string `yaml:"types"`
		} `yaml:"pull_request_target"`
		Review struct {
			Types []string `yaml:"types"`
		} `yaml:"pull_request_review"`
	} `yaml:"on"`
	Permissions map[string]string            `yaml:"permissions"`
	Jobs        map[string]releaseControlJob `yaml:"jobs"`
}

type releaseControlJob struct {
	Needs       []string          `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Environment struct {
		Name string `yaml:"name"`
	} `yaml:"environment"`
	Env   map[string]string `yaml:"env"`
	Steps []struct {
		Name string            `yaml:"name"`
		Run  string            `yaml:"run"`
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
		Env  map[string]string `yaml:"env"`
	} `yaml:"steps"`
}

func TestReleaseReadinessWorkflowBoundary(t *testing.T) {
	workflow := loadReleaseControlWorkflow(t, "release-pr-gate.yml")
	if len(workflow.On.Target.Paths) != 0 {
		t.Fatal("required readiness status must not use workflow path filters")
	}
	events := []string{"opened", "synchronize", "reopened", "edited", "labeled", "unlabeled", "converted_to_draft"}
	for _, event := range events {
		if !slices.Contains(workflow.On.Target.Types, event) {
			t.Errorf("missing readiness event %s", event)
		}
	}
	for _, event := range []string{"submitted", "edited", "dismissed"} {
		if !slices.Contains(loadReleaseControlWorkflow(t, "release-review-event.yml").On.Review.Types, event) {
			t.Errorf("missing review revocation event %s", event)
		}
	}
	if len(workflow.On.Review.Types) != 0 {
		t.Fatal("privileged readiness workflow must not run in review PR context")
	}
	notifier := loadReleaseControlWorkflow(t, "release-review-event.yml")
	if len(notifier.Permissions) != 0 {
		t.Fatal("review notifier must not have token permissions")
	}
	gate := workflow.Jobs["gate"]
	if gate.Permissions["statuses"] != "write" || gate.Permissions["contents"] != "read" {
		t.Fatal("metadata writer must have status write without repository content write")
	}
	for _, step := range gate.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") &&
			step.With["ref"] != "${{ github.event.pull_request.base.sha || github.sha }}" {
			t.Fatal("readiness must execute protected tooling rather than PR code")
		}
	}
}

func TestReleaseTagApprovalBoundary(t *testing.T) {
	workflow := loadReleaseControlWorkflow(t, "release-tag.yml")
	preflight := workflow.Jobs["preflight"]
	for permission, value := range preflight.Permissions {
		if value != "read" {
			t.Errorf("preflight has non-read permission %s=%s", permission, value)
		}
	}
	for _, step := range preflight.Steps {
		for _, value := range step.Env {
			if strings.Contains(value, "secrets.") {
				t.Error("preflight must not access signing secrets")
			}
		}
	}
}

func TestReleaseReadinessInvalidatesBeforeCheckout(t *testing.T) {
	gate := loadReleaseControlWorkflow(t, "release-pr-gate.yml").Jobs["gate"]
	if len(gate.Steps) == 0 || gate.Steps[0].Uses != "" || gate.Steps[0].Run == "" {
		t.Fatal("readiness must mark the candidate pending before checkout can fail")
	}
	first := gate.Steps[0]
	if first.Env["EXPECTED_HEAD_SHA"] != "${{ needs.resolve.outputs.head_sha }}" {
		t.Fatal("initial pending status must target the resolved candidate")
	}
	f := newReadinessFixture(t)
	script := filepath.Join(f.dir, "pending.sh")
	if err := os.WriteFile(script, []byte(first.Run), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stop before checkout to model a checkout failure after the first step.
	f.check(t, script, "", "GITHUB_SERVER_URL=https://github.com")
	calls := strings.TrimSpace(readTestFile(t, filepath.Join(f.dir, "calls")))
	want := "/statuses/" + readinessHead + " --method POST -f context=Release Readiness -f state=pending"
	if strings.Contains(calls, "\n") || !strings.Contains(calls, want) {
		t.Fatalf("checkout failure did not leave the exact candidate pending: %s", calls)
	}
}

func TestReleaseTagCandidateBinding(t *testing.T) {
	tagger := loadReleaseControlWorkflow(t, "release-tag.yml").Jobs["release-tag"]
	if !slices.Contains(tagger.Needs, "preflight") || tagger.Environment.Name != "release-manual" {
		t.Fatal("tag creation must require candidate preflight and protected approval")
	}
	for name, output := range map[string]string{
		"APPROVED_PR_NUMBER": "pr_number", "APPROVED_HEAD_SHA": "head_sha",
		"APPROVED_MERGE_SHA": "merge_sha", "APPROVED_VERSION": "version",
	} {
		if tagger.Env[name] != "${{ needs.preflight.outputs."+output+" }}" {
			t.Errorf("%s is not bound to the approved candidate", name)
		}
	}
	revalidated := false
	for _, step := range tagger.Steps {
		if step.Name == "Revalidate approved candidate before signing secrets" {
			revalidated = step.Env["PREFLIGHT_ONLY"] == "1"
		}
		for _, value := range step.Env {
			if strings.Contains(value, "secrets.") && !revalidated {
				t.Error("signing secrets are accessed before candidate revalidation")
			}
		}
	}
	if !revalidated {
		t.Fatal("approved release candidate is not revalidated")
	}
}

func TestReleaseTagCheckoutPins(t *testing.T) {
	workflow := loadReleaseControlWorkflow(t, "release-tag.yml")
	for _, job := range []releaseControlJob{workflow.Jobs["preflight"], workflow.Jobs["release-tag"]} {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["ref"] != "${{ github.sha }}" {
				t.Error("release checkout is not pinned to the triggering commit")
			}
		}
	}
}

func loadReleaseControlWorkflow(t *testing.T, name string) releaseControlWorkflow {
	t.Helper()
	var workflow releaseControlWorkflow
	data := readTestFile(t, "../../.github/workflows/"+name)
	if err := yaml.Unmarshal([]byte(data), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func TestReleaseReviewRefreshIsSerialized(t *testing.T) {
	for _, apiFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "candidate resolved", true: "lookup fails"}[apiFailure], func(t *testing.T) {
			f := newReadinessFixture(t)
			f.sync(t)
			f.writeJSON(t, "pulls.json", [][]map[string]interface{}{{f.pr}})
			f.writeJSON(t, "event.json", map[string]interface{}{
				"workflow_run": map[string]interface{}{"pull_requests": []map[string]interface{}{{
					"number": 6,
					"head": map[string]interface{}{
						"ref": "release-please--branches--main", "sha": readinessHead,
						"repo": map[string]int{"id": 42},
					},
					"base": map[string]string{"ref": "main"},
				}}},
			})
			workflow := loadReleaseControlWorkflow(t, "release-pr-gate.yml")
			script := filepath.Join(f.dir, "resolve.sh")
			if err := os.WriteFile(script, []byte(workflow.Jobs["resolve"].Steps[0].Run), 0o600); err != nil {
				t.Fatal(err)
			}
			env := []string{
				"EVENT_NAME=workflow_run", "REPOSITORY_ID=42", "GITHUB_EVENT_PATH=" + filepath.Join(f.dir, "event.json"),
				"GITHUB_SERVER_URL=https://github.com",
			}
			f.check(t, script, "", env...)
			// Candidate resolution is read-only; only the serialized writer mutates status.
			if _, err := os.Stat(filepath.Join(f.dir, "calls")); !os.IsNotExist(err) {
				t.Fatal("associated candidate resolution made an API call")
			}
			want := ""
			if apiFailure {
				env = append(env, "FAIL_API=pulls/6")
				want = "fixture API failure"
			}
			f.check(t, "publish-release-readiness.sh", want, env...)
			calls := readTestFile(t, filepath.Join(f.dir, "calls"))
			if !strings.Contains(calls, "-f state=pending") ||
				(apiFailure && !strings.Contains(calls, "-f state=failure")) {
				t.Fatalf("review refresh did not invalidate previous readiness: %s", calls)
			}
		})
	}
}
