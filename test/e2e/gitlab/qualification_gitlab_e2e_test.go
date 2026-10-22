//go:build e2e

package gitlabe2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/openbao/openbao/api/v2"
)

func TestOpenBaoPluginSyncsToGitLabProjectVariables(t *testing.T) {
	// Each subtest mounts a fresh plugin. Do not run these cases in parallel.
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"basic_lifecycle", testGitLabBasicLifecycle},
		{"file_variables", func(t *testing.T) { testGitLabVariableShape(t, gitlab.VariableTypeFile, false) }},
		{"hidden_variables", func(t *testing.T) { testGitLabVariableShape(t, gitlab.VariableTypeEnvVar, true) }},
		{"visible_to_hidden_rejected", testGitLabHiddenUpdateRejected},
		{"environment_scope_isolation", testGitLabIndependentScopes},
		{"same_source_scope_identity", testGitLabSameSourceScopes},
		{"ownership_loss", func(t *testing.T) { testGitLabOwnershipSafety(t, true) }},
		{"foreign_collision", func(t *testing.T) { testGitLabOwnershipSafety(t, false) }},
		{"retain_delete_mode", func(t *testing.T) { testGitLabRetainedDelete(t, "retain") }},
		{"orphan_delete_mode", func(t *testing.T) { testGitLabRetainedDelete(t, "orphan") }},
		{"credential_refresh", testGitLabCredentialRefresh},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type gitLabFixture struct {
	ctx    context.Context
	bao    *api.Client
	remote gitLabClient
}

func newGitLabFixture(t *testing.T) gitLabFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	bao := newOpenBaoClient(t)
	waitForOpenBao(t, ctx, bao)
	registerPlugin(t, bao)
	mountPlugin(t, bao)
	t.Cleanup(func() {
		if err := bao.Sys().Unmount(mountPath); err != nil {
			t.Errorf("unmount qualification plugin: %v", err)
		}
	})
	writeGitLabDestination(t, bao)
	acknowledgeRestoreGuard(t, bao)
	disableEventDispatch(t, bao)
	return gitLabFixture{ctx: ctx, bao: bao, remote: newGitLabClient(t)}
}

func qualificationVariableKey(t *testing.T, remote gitLabClient) string {
	t.Helper()
	key := fmt.Sprintf("OPENBAO_QUALIFICATION_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := remote.deleteVariable(ctx, key); err != nil {
			t.Errorf("clean up qualification variable: %v", err)
		}
	})
	return key
}

func testGitLabVariableShape(t *testing.T, variableType string, hidden bool) {
	fixture := newGitLabFixture(t)
	key := qualificationVariableKey(t, fixture.remote)
	initial, updated := "first line\n$E2E_REFERENCE\n", "second line\n$E2E_REFERENCE\n"
	if hidden {
		initial, updated = "fixture_hidden_token_123", "fixture_hidden_token_456"
	}
	request := associationRequest()
	request[gitlab.ConfigKeyVariableType] = variableType
	request[gitlab.ConfigKeyVariableRaw] = false
	request[gitlab.ConfigKeyProtected] = true
	request[gitlab.ConfigKeyHidden] = hidden
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, initial)
	write(t, fixture.bao, mountPath+"/associations/app/db", request)
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariable(t, fixture.ctx, fixture.remote, key, initial, "1")
	assertGitLabVariableAttributes(t, fixture.ctx, fixture.remote, key, true, hidden, false, variableType)
	assertGitLabHidden(t, fixture, key, hidden)
	if hidden {
		assertGitLabFixtureHiddenPayload(t, fixture, key, initial)
	}
	update := writeSourceAt(t, fixture.bao, "app/db", key, updated)
	drainQueue(t, fixture.bao, 1)
	if hidden {
		operationID := singleQualificationOperation(t, metadataOperationIDs(t, update))
		logGitLabHiddenRotation(t, fixture, key, operationID, initial, updated)
	}
	assertGitLabVariable(t, fixture.ctx, fixture.remote, key, updated, "2")
	assertGitLabVariableAttributes(t, fixture.ctx, fixture.remote, key, true, hidden, false, variableType)
	assertGitLabHidden(t, fixture, key, hidden)
	if hidden {
		assertGitLabFixtureHiddenPayload(t, fixture, key, updated)
		assertGitLabHiddenConvergence(t, fixture, key, request)
		assertGitLabHiddenExternalChange(t, fixture, key, request)
	}
	deletePath(t, fixture.bao, mountPath+"/data/app/db")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableMissing(t, fixture.ctx, fixture.remote, key)
}

func assertGitLabHiddenConvergence(t *testing.T, fixture gitLabFixture, key string, request map[string]interface{}) {
	t.Helper()
	plan := write(t, fixture.bao, mountPath+"/associations/app/db/plan", request)
	if plan.Data["action"] != "noop" {
		t.Fatalf("stable hidden plan action = %v, want noop", plan.Data["action"])
	}
	assertReconcilePlan(t, fixture.bao, key, string(domain.SyncStateSynced))
	reconciled := write(t, fixture.bao, mountPath+"/reconcile/app/db", map[string]interface{}{})
	assertReconcileObject(t, reconciled, key, string(domain.SyncStateSynced))
	object := objectByID(t, objectsFromSecret(t, reconciled.Data["objects"]), key)
	if object["verification"] != "metadata" {
		t.Fatalf("hidden reconcile verification = %v, want metadata", object["verification"])
	}
	drainQueue(t, fixture.bao, 0)
}

func assertGitLabHiddenExternalChange(t *testing.T, fixture gitLabFixture, key string, request map[string]interface{}) {
	t.Helper()
	externalValue := "fixture_external_hidden_789"
	variable := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	variable.Value = externalValue
	if err := fixture.remote.writeVariable(fixture.ctx, http.MethodPut, variable); err != nil {
		t.Fatalf("prepare external hidden value change: %v", err)
	}
	assertGitLabHiddenConvergence(t, fixture, key, request)
	assertGitLabFixtureHiddenPayload(t, fixture, key, externalValue)
	t.Log("hidden external value change remains undetected when ownership and payload-hash metadata are unchanged")
}

func assertGitLabFixtureHiddenPayload(t *testing.T, fixture gitLabFixture, key, expected string) {
	t.Helper()
	directory, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- fixed Docker arguments select the local fixture by its directory and service labels.
	list := exec.CommandContext(fixture.ctx, "docker", "ps",
		"--filter", "label=com.docker.compose.project.working_dir="+directory,
		"--filter", "label=com.docker.compose.service=gitlab", "--format", "{{.ID}}")
	output, err := list.Output()
	if err != nil {
		t.Fatalf("find GitLab fixture for hidden payload verification: %v", err)
	}
	containers := strings.Fields(string(output))
	if len(containers) != 1 {
		t.Fatalf("hidden payload verification requires one GitLab container for this fixture directory, got %d",
			len(containers))
	}
	sum := sha256.Sum256([]byte(expected))
	// The fixture administrator compares the stored value hash. Only booleans leave GitLab.
	const script = `require "digest"
project = Project.find_by_full_path(ENV.fetch("E2E_VERIFY_PROJECT"))
variable = project&.variables&.find_by(
  key: ENV.fetch("E2E_VERIFY_KEY"), environment_scope: ENV.fetch("E2E_VERIFY_SCOPE"))
puts JSON.generate(found: !variable.nil?, hidden: variable&.hidden?,
  matches: variable && Digest::SHA256.hexdigest(variable.value) == ENV.fetch("E2E_VERIFY_SHA256"))`
	// #nosec G204 -- the Ruby program is fixed; metadata and the expected hash are separate arguments without a shell.
	probe := exec.CommandContext(fixture.ctx, "docker", "exec",
		"-e", "E2E_VERIFY_PROJECT="+fixture.remote.projectPath,
		"-e", "E2E_VERIFY_KEY="+key,
		"-e", "E2E_VERIFY_SCOPE="+fixture.remote.environmentScope,
		"-e", "E2E_VERIFY_SHA256="+hex.EncodeToString(sum[:]),
		containers[0], "gitlab-rails", "runner", script)
	output, err = probe.Output()
	if err != nil {
		t.Fatalf("run fixture-only hidden payload verification: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var proof struct {
		Found   bool `json:"found"`
		Hidden  bool `json:"hidden"`
		Matches bool `json:"matches"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &proof); err != nil {
		t.Fatalf("decode boolean-only hidden payload proof: %v", err)
	}
	if !proof.Found || !proof.Hidden || !proof.Matches {
		t.Fatalf("fixture hidden payload proof: found=%t hidden=%t matches=%t", proof.Found, proof.Hidden, proof.Matches)
	}
	t.Log("fixture-only hidden payload proof: found=true hidden=true matches=true")
}

func testGitLabHiddenUpdateRejected(t *testing.T) {
	fixture := newGitLabFixture(t)
	key := qualificationVariableKey(t, fixture.remote)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "fixture_visible_token_123")
	write(t, fixture.bao, mountPath+"/associations/app/db", associationRequest())
	drainQueue(t, fixture.bao, 1)
	before := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	request := associationRequest()
	request[gitlab.ConfigKeyHidden] = true
	assertGitLabAssociationPlan(t, fixture.bao, request, "blocked", "validation")
	association := write(t, fixture.bao, mountPath+"/associations/app/db", request)
	operationID := singleQualificationOperation(t, stringSlice(t, association.Data["sync_operation_ids"]))
	drainQueue(t, fixture.bao, 1)
	assertFailedGitLabOperation(t, fixture.bao, operationID, "upsert")
	assertStatusObject(t, fixture.bao, key, "VALIDATION_ERROR", "validation")
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
}

func testGitLabIndependentScopes(t *testing.T) {
	fixture := newGitLabFixture(t)
	key, left, right := qualificationScopedVariable(t, fixture)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "left_initial")
	writeSourceSyncEnabledAt(t, fixture.bao, "app/other", key, "right_initial")
	leftRequest, rightRequest := associationRequest(), associationRequest()
	leftRequest[gitlab.ConfigKeyEnvironmentScope] = left.environmentScope
	rightRequest[gitlab.ConfigKeyEnvironmentScope] = right.environmentScope
	leftAssociation := write(t, fixture.bao, mountPath+"/associations/app/db", leftRequest)
	rightAssociation := write(t, fixture.bao, mountPath+"/associations/app/other", rightRequest)
	if leftAssociation.Data["association_id"] == rightAssociation.Data["association_id"] {
		t.Fatal("independent scope associations must have distinct IDs")
	}
	drainQueue(t, fixture.bao, 2)
	assertGitLabVariableAt(t, fixture.ctx, left, "app/db", key, "left_initial", "1")
	assertGitLabVariableAt(t, fixture.ctx, right, "app/other", key, "right_initial", "1")
	before := snapshotGitLabVariable(t, fixture, right, key)
	writeSource(t, fixture.bao, key, "left_updated")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableAt(t, fixture.ctx, left, "app/db", key, "left_updated", "2")
	assertGitLabVariableUnchanged(t, fixture, right, key, before)
	deletePath(t, fixture.bao, mountPath+"/data/app/db")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableMissing(t, fixture.ctx, left, key)
	assertGitLabVariableUnchanged(t, fixture, right, key, before)
	deletePath(t, fixture.bao, mountPath+"/data/app/other")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableMissing(t, fixture.ctx, right, key)
}

func qualificationScopedVariable(t *testing.T, fixture gitLabFixture) (string, gitLabClient, gitLabClient) {
	t.Helper()
	left, right := fixture.remote, fixture.remote
	left.environmentScope, right.environmentScope = "preview-left", "preview-right"
	key := qualificationVariableKey(t, left)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := right.deleteVariable(ctx, key); err != nil {
			t.Errorf("clean up second scope: %v", err)
		}
	})
	return key, left, right
}

func testGitLabSameSourceScopes(t *testing.T) {
	fixture := newGitLabFixture(t)
	key, left, right := qualificationScopedVariable(t, fixture)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "shared_initial")
	leftRequest, rightRequest := associationRequest(), associationRequest()
	leftRequest[gitlab.ConfigKeyEnvironmentScope] = left.environmentScope
	rightRequest[gitlab.ConfigKeyEnvironmentScope] = right.environmentScope
	leftAssociation := write(t, fixture.bao, mountPath+"/associations/app/db", leftRequest)
	rightAssociation := write(t, fixture.bao, mountPath+"/associations/app/db", rightRequest)
	leftID, leftOK := leftAssociation.Data["association_id"].(string)
	rightID, rightOK := rightAssociation.Data["association_id"].(string)
	if !leftOK || !rightOK || leftID == "" || rightID == "" || leftID == rightID {
		t.Fatal("same-source scope associations must have distinct IDs")
	}
	drainQueue(t, fixture.bao, 2)
	assertGitLabVariable(t, fixture.ctx, left, key, "shared_initial", "1")
	assertGitLabVariable(t, fixture.ctx, right, key, "shared_initial", "1")
	rightBefore := snapshotGitLabVariable(t, fixture, right, key)
	writeExpectError(t, fixture.bao, mountPath+"/associations/app/db/disable",
		map[string]interface{}{"destination": "gitlab/local"}, "ambiguous")
	leftRequest[gitlab.ConfigKeyProtected] = true
	updated := write(t, fixture.bao, mountPath+"/associations/app/db", leftRequest)
	if updated.Data["association_id"] != leftID {
		t.Fatal("scoped attribute update selects a different association")
	}
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableAttributes(t, fixture.ctx, left, key, true, false, true, gitlab.VariableTypeEnvVar)
	assertGitLabVariableUnchanged(t, fixture, right, key, rightBefore)
	leftBefore := snapshotGitLabVariable(t, fixture, left, key)
	deletePath(t, fixture.bao, fmt.Sprintf("%s/associations/app/db/%s", mountPath, leftID))
	assertGitLabAssociationRemoved(t, fixture.bao, leftID, rightID)
	assertGitLabVariableUnchanged(t, fixture, left, key, leftBefore)
	assertGitLabVariableUnchanged(t, fixture, right, key, rightBefore)
	writeSource(t, fixture.bao, key, "shared_updated")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariable(t, fixture.ctx, right, key, "shared_updated", "2")
	assertGitLabVariableUnchanged(t, fixture, left, key, leftBefore)
	deletePath(t, fixture.bao, mountPath+"/data/app/db")
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariableMissing(t, fixture.ctx, right, key)
	assertGitLabVariableUnchanged(t, fixture, left, key, leftBefore)
}

func assertGitLabAssociationRemoved(t *testing.T, bao *api.Client, removedID, retainedID string) {
	t.Helper()
	removed, err := bao.Logical().Read(fmt.Sprintf("%s/associations/app/db/%s", mountPath, removedID))
	if err != nil || removed != nil {
		t.Fatalf("removed association is still readable: read error = %v", err)
	}
	retained, err := bao.Logical().Read(fmt.Sprintf("%s/associations/app/db/%s", mountPath, retainedID))
	if err != nil || retained == nil || retained.Data["id"] != retainedID {
		t.Fatalf("retained association is missing or changed: read error = %v", err)
	}
}

func testGitLabOwnershipSafety(t *testing.T, initiallyOwned bool) {
	fixture := newGitLabFixture(t)
	key := qualificationVariableKey(t, fixture.remote)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "fixture_managed_token_123")
	if initiallyOwned {
		write(t, fixture.bao, mountPath+"/associations/app/db", associationRequest())
		drainQueue(t, fixture.bao, 1)
		assertGitLabVariable(t, fixture.ctx, fixture.remote, key, "fixture_managed_token_123", "1")
	}
	foreign := gitLabVariable{
		Key: key, Value: "fixture_foreign_value", Description: "External owner: preserve this description",
		EnvironmentScope: fixture.remote.environmentScope, VariableRaw: true, VariableType: gitlab.VariableTypeEnvVar,
	}
	method := http.MethodPost
	if initiallyOwned {
		method = http.MethodPut
	}
	if err := fixture.remote.writeVariable(fixture.ctx, method, foreign); err != nil {
		t.Fatalf("prepare foreign variable: %v", err)
	}
	before := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	request := associationRequest()
	request[gitlab.ConfigKeyHidden] = !initiallyOwned
	assertGitLabAssociationPlan(t, fixture.bao, request, "conflict", "collision")
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
	var operationID string
	if initiallyOwned {
		update := writeSourceAt(t, fixture.bao, "app/db", key, "fixture_managed_token_456")
		operationID = singleQualificationOperation(t, metadataOperationIDs(t, update))
	} else {
		association := write(t, fixture.bao, mountPath+"/associations/app/db", request)
		operationID = singleQualificationOperation(t, stringSlice(t, association.Data["sync_operation_ids"]))
	}
	drainQueue(t, fixture.bao, 1)
	assertFailedGitLabOperation(t, fixture.bao, operationID, "upsert")
	assertStatusObject(t, fixture.bao, key, "REMOTE_OWNERSHIP_LOST", "ownership")
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
	deleted := deletePath(t, fixture.bao, mountPath+"/data/app/db")
	deleteID := singleQualificationOperation(t, metadataOperationIDs(t, deleted))
	drainQueue(t, fixture.bao, 1)
	assertFailedGitLabOperation(t, fixture.bao, deleteID, "delete")
	assertStatusObject(t, fixture.bao, key, "REMOTE_OWNERSHIP_LOST", "ownership")
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
}

func testGitLabRetainedDelete(t *testing.T, mode string) {
	fixture := newGitLabFixture(t)
	key := qualificationVariableKey(t, fixture.remote)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "fixture_retained_payload")
	request := associationRequest()
	request["delete_mode"] = mode
	write(t, fixture.bao, mountPath+"/associations/app/db", request)
	drainQueue(t, fixture.bao, 1)
	before := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	deleted := deletePath(t, fixture.bao, mountPath+"/data/app/db")
	if ids := metadataOperationIDs(t, deleted); len(ids) != 0 {
		t.Fatalf("retained delete enqueues %d operations, want zero", len(ids))
	}
	drainQueue(t, fixture.bao, 0)
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
}

func testGitLabCredentialRefresh(t *testing.T) {
	fixture := newGitLabFixture(t)
	key := qualificationVariableKey(t, fixture.remote)
	writeSourceSyncEnabledAt(t, fixture.bao, "app/db", key, "initial")
	write(t, fixture.bao, mountPath+"/associations/app/db", associationRequest())
	drainQueue(t, fixture.bao, 1)
	before := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	write(t, fixture.bao, mountPath+"/destinations/gitlab/local", map[string]interface{}{
		gitlab.ConfigKeyToken: "glpat-intentionally-invalid-e2e-token",
	})
	writeSource(t, fixture.bao, key, "updated")
	drainQueue(t, fixture.bao, 1)
	assertStatusObject(t, fixture.bao, key, "DESTINATION_AUTH_ERROR", "authn")
	assertGitLabVariableUnchanged(t, fixture, fixture.remote, key, before)
	writeGitLabDestination(t, fixture.bao)
	assertDestinationHealthy(t, fixture.bao)
	write(t, fixture.bao, mountPath+"/associations/app/db/sync", map[string]interface{}{
		"destination": "gitlab/local",
	})
	drainQueue(t, fixture.bao, 1)
	assertGitLabVariable(t, fixture.ctx, fixture.remote, key, "updated", "2")
}

func assertGitLabAssociationPlan(t *testing.T, bao *api.Client, request map[string]interface{}, action, class string) {
	t.Helper()
	plan := write(t, bao, mountPath+"/associations/app/db/plan", request)
	if plan.Data["action"] != action {
		t.Fatalf("plan action = %v, want %s", plan.Data["action"], action)
	}
	objects := objectsFromSecret(t, plan.Data["objects"])
	if len(objects) != 1 || objects[0]["action"] != action || objects[0]["error_class"] != class {
		t.Fatalf("plan must include one object with action=%s and error_class=%s", action, class)
	}
}

func logGitLabHiddenRotation(t *testing.T, fixture gitLabFixture, key, operationID, initial, updated string) {
	t.Helper()
	status, err := fixture.bao.Logical().Read(mountPath + "/status/app/db")
	if err != nil || status == nil {
		t.Fatalf("read hidden rotation status: %v", err)
	}
	object := objectByID(t, objectsFromSecret(t, status.Data["objects"]), key)
	operation, err := fixture.bao.Logical().Read(mountPath + "/queue/" + operationID)
	if err != nil {
		t.Fatalf("read hidden rotation operation: %v", err)
	}
	operationState := "completed"
	if operation != nil {
		operationState = fmt.Sprint(operation.Data["state"])
	}
	remote := snapshotGitLabVariable(t, fixture, fixture.remote, key)
	metadata := variableMetadataFromDescription(t, remote.Description)
	t.Logf("hidden rotation: operation=%s status=%v error_class=%v remote_source_version=%d "+
		"hidden=%t masked=%t raw=%t value_available=%t matches_initial=%t matches_updated=%t",
		operationState, object["state"], object["last_error_class"], metadata.SourceVersion,
		remote.Hidden, remote.Masked, remote.VariableRaw, !remote.valueUnavailable,
		remote.Value == initial, remote.Value == updated)
	if object["state"] != string(domain.SyncStateSynced) || object["last_error_class"] != "" {
		t.Fatalf("hidden rotation failed: operation=%s status=%v error_class=%v",
			operationState, object["state"], object["last_error_class"])
	}
}

func singleQualificationOperation(t *testing.T, ids []string) string {
	t.Helper()
	if len(ids) != 1 {
		t.Fatalf("operation count = %d, want one", len(ids))
	}
	return ids[0]
}

func assertFailedGitLabOperation(t *testing.T, bao *api.Client, id, operationType string) {
	t.Helper()
	operation, err := bao.Logical().Read(mountPath + "/queue/" + id)
	if err != nil || operation == nil {
		t.Fatalf("read failed operation: %v", err)
	}
	if operation.Data["state"] != "failed_terminal" || operation.Data["type"] != operationType {
		t.Fatalf("operation state/type = %v/%v, want failed_terminal/%s",
			operation.Data["state"], operation.Data["type"], operationType)
	}
}

func snapshotGitLabVariable(t *testing.T, fixture gitLabFixture, remote gitLabClient, key string) gitLabVariable {
	t.Helper()
	variable, err := remote.getVariable(fixture.ctx, key)
	if err != nil || variable == nil {
		t.Fatalf("read qualification variable: %v", err)
	}
	return *variable
}

func assertGitLabVariableUnchanged(
	t *testing.T,
	fixture gitLabFixture,
	remote gitLabClient,
	key string,
	before gitLabVariable,
) {
	t.Helper()
	if after := snapshotGitLabVariable(t, fixture, remote, key); after != before {
		t.Fatal("GitLab variable value, description, identity, or attributes changed unexpectedly")
	}
}

func assertGitLabHidden(t *testing.T, fixture gitLabFixture, key string, hidden bool) {
	t.Helper()
	if variable := snapshotGitLabVariable(t, fixture, fixture.remote, key); variable.Hidden != hidden {
		t.Fatalf("GitLab hidden = %t, want %t", variable.Hidden, hidden)
	}
}

func (c gitLabClient) writeVariable(ctx context.Context, method string, variable gitLabVariable) error {
	form := url.Values{}
	form.Set("key", variable.Key)
	form.Set("value", variable.Value)
	form.Set("description", variable.Description)
	form.Set("environment_scope", c.environmentScope)
	form.Set("protected", strconv.FormatBool(variable.Protected))
	form.Set("masked", strconv.FormatBool(variable.Masked))
	form.Set("raw", strconv.FormatBool(variable.VariableRaw))
	form.Set("variable_type", variable.VariableType)
	endpoint := c.variableURL(variable.Key)
	if method == http.MethodPost {
		endpoint = c.baseURL + "/api/v4/projects/" + url.PathEscape(c.projectPath) + "/variables"
	}
	return c.sendVariableForm(ctx, method, endpoint, form)
}

func (c gitLabClient) sendVariableForm(ctx context.Context, method, endpoint string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req) //nolint:gosec // Opt-in e2e endpoint is local Docker/loopback GitLab.
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("gitlab %s variable status %d", method, resp.StatusCode)
	}
	return nil
}
