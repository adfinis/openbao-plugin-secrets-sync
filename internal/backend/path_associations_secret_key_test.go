package backend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/kubernetessecrets"
)

func TestAssociationSecretKeyQueuesAndSyncsPerSourceKey(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()

	planResp := env.update("associations/app/db/plan", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, planResp)
	assertResponseValue(t, planResp, "action", providers.PlanActionCreate)
	assertResponseValue(t, planResp, "granularity", syncGranularitySecretKey)
	planObjects := objectsByIDFromRaw(t, planResp.Data["objects"])
	assertPlanObject(t, planObjects, "password", "prod/app/db/password")
	assertPlanObject(t, planObjects, "username", "prod/app/db/username")
	if strings.Contains(fmt.Sprint(planResp.Data), "initial") || strings.Contains(fmt.Sprint(planResp.Data), "appuser") {
		t.Fatalf("secret-key plan response contains secret value: %#v", planResp.Data)
	}

	associationResp := env.createFakeSecretKeyAssociation(deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 2 {
		t.Fatalf("secret-key association operation IDs = %v, want two operations", operationIDs)
	}
	assertOperationObjectIDs(t, env.storage, operationIDs, 1, outboxStatePending, []string{"password", "username"})

	env.runPeriodicAllowed("periodic")
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	assertResponseValue(t, statusResp, "state", string(domain.SyncStateSynced))
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	assertSecretKeySyncedStatusObject(
		t,
		statusObjects,
		"password",
		associationID,
		"prod/app/db/password",
		operationIDs[0],
	)
	assertSecretKeySyncedStatusObject(
		t,
		statusObjects,
		"username",
		associationID,
		"prod/app/db/username",
		operationIDs[1],
	)

	updateResp := env.writeAppDBSecretData(map[string]interface{}{
		"password": "rotated",
		"username": "appuser",
	})
	updateMetadata := updateResp.Data["metadata"].(map[string]interface{})
	updateOperationIDs := operationIDsFromMetadata(t, updateMetadata)
	if len(updateOperationIDs) != 2 {
		t.Fatalf("secret-key update operation IDs = %v, want two operations", updateOperationIDs)
	}
	assertOperationObjectIDs(t, env.storage, updateOperationIDs, 2, outboxStatePending, []string{"password", "username"})
}

func TestAssociationSecretKeyRawFormat(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"PASSWORD": "initial",
	})
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()

	resp := env.update("associations/app/db/plan", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        rawAssociationFormat,
	})
	assertNoErrorResponse(t, resp)
	objects := objectsByIDFromRaw(t, resp.Data["objects"])
	object := objects["PASSWORD"]
	if got := object["payload_bytes"]; got != len("initial") {
		t.Fatalf("raw payload bytes = %v, want %d", got, len("initial"))
	}
	assertNoPayloadHash(t, object)
}

func TestAssociationRawFormatRequiresSecretKey(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncGranularitySecretPath,
		"format":        rawAssociationFormat,
	})
	if resp == nil || !resp.IsError() {
		t.Fatalf("raw secret-path association response = %#v, want error", resp)
	}
}

func TestAssociationGitLabSecretKeyRawFormat(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"APP_PASSWORD": "initial",
	})
	writeResp := env.update("destinations/gitlab/prod", map[string]interface{}{
		gitlab.ConfigKeyProjectID: "platform/app",
		gitlab.ConfigKeyToken:     "glpat-secret",
	})
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected gitlab destination write error: %v", writeResp.Error())
	}
	env.enableAppDBSourceSync()

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		"name_template":                  "{{ key }}",
		"granularity":                    syncGranularitySecretKey,
		"format":                         rawAssociationFormat,
		"delete_mode":                    deleteModeRetain,
		gitlab.ConfigKeyEnvironmentScope: "production",
	})
	assertNoErrorResponse(t, resp)
	operationIDs := operationIDsFromResponse(t, resp)
	if len(operationIDs) != 1 {
		t.Fatalf("gitlab operation IDs = %v, want one operation", operationIDs)
	}
	operation := assertOutboxOperation(t, env.storage, operationIDs[0], 1, outboxStatePending)
	if got := operation.ObjectID; got != "APP_PASSWORD" {
		t.Fatalf("gitlab object ID = %s, want APP_PASSWORD", got)
	}
	if got := resp.Data["destination_ref"]; got != "gitlab/prod" {
		t.Fatalf("gitlab destination_ref = %v, want gitlab/prod", got)
	}
	if got := resp.Data["format"]; got != rawAssociationFormat {
		t.Fatalf("gitlab association format = %v, want %s", got, rawAssociationFormat)
	}
}

func TestAssociationSecretKeyReservesRenderedNamePattern(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()
	firstResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, firstResp)

	env.update("data/app", map[string]interface{}{
		"data": map[string]interface{}{
			"password": "other",
		},
	})
	env.enableSourceSync("app")
	secondResp := env.update("associations/app", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "/app/db/{{ key }}/",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	if secondResp == nil || !secondResp.IsError() {
		t.Fatalf("rendered-name collision response = %#v, want error", secondResp)
	}
	if !strings.Contains(secondResp.Error().Error(), "already reserved") {
		t.Fatalf("rendered-name collision error = %q, want reservation error", secondResp.Error().Error())
	}
}

func TestAssociationSecretKeyReservesConcreteRenderedNames(t *testing.T) {
	env := newBackendTestEnv(t)

	env.update("data/left", map[string]interface{}{
		"data": map[string]interface{}{
			"a": "left",
		},
	})
	env.createFakeDestination("default")
	env.enableSourceSync("left")
	firstResp := env.update("associations/left", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "a{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, firstResp)

	env.update("data/right", map[string]interface{}{
		"data": map[string]interface{}{
			"a": "right",
		},
	})
	env.enableSourceSync("right")
	secondResp := env.update("associations/right", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "{{ key }}a",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	if secondResp == nil || !secondResp.IsError() {
		t.Fatalf("concrete-name collision response = %#v, want error", secondResp)
	}
	if !strings.Contains(secondResp.Error().Error(), "already reserved") {
		t.Fatalf("concrete-name collision error = %q, want reservation error", secondResp.Error().Error())
	}
}

func TestAssociationSecretKeySourceWriteRejectsNewConcreteNameCollision(t *testing.T) {
	env := newBackendTestEnv(t)

	env.update("data/left", map[string]interface{}{
		"data": map[string]interface{}{
			"x": "left",
		},
	})
	env.createFakeDestination("default")
	env.enableSourceSync("left")
	firstResp := env.update("associations/left", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "a{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, firstResp)

	env.update("data/right", map[string]interface{}{
		"data": map[string]interface{}{
			"b": "right",
		},
	})
	env.enableSourceSync("right")
	secondResp := env.update("associations/right", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "{{ key }}x",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, secondResp)

	writeResp := env.update("data/right", map[string]interface{}{
		"data": map[string]interface{}{
			"a": "blocked",
		},
	})
	if writeResp == nil || !writeResp.IsError() {
		t.Fatalf("source write collision response = %#v, want error", writeResp)
	}
	if !strings.Contains(writeResp.Error().Error(), "already reserved") {
		t.Fatalf("source write collision error = %q, want reservation error", writeResp.Error().Error())
	}
	readResp := env.read("data/right")
	assertNoErrorResponse(t, readResp)
	metadata := readResp.Data["metadata"].(map[string]interface{})
	if got := metadata["version"]; got != 1 {
		t.Fatalf("right source version = %v, want 1", got)
	}
}

func TestAssociationSecretKeyDeleteModeQueuesPerSourceKeyDeletes(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	env.createFakeSecretKeyAssociation(deleteModeDelete)
	env.runPeriodicAllowed("periodic upsert")

	deleteResp := env.delete("data/app/db")
	assertNoErrorResponse(t, deleteResp)
	deleteOperationIDs := operationIDsFromMetadata(t, deleteResp.Data["metadata"].(map[string]interface{}))
	if len(deleteOperationIDs) != 2 {
		t.Fatalf("secret-key delete operation IDs = %v, want two operations", deleteOperationIDs)
	}
	for _, operationID := range deleteOperationIDs {
		operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
		if got := operation.Type; got != outbox.OperationTypeDelete {
			t.Fatalf("secret-key delete operation type = %s, want %s", got, outbox.OperationTypeDelete)
		}
	}
	assertOperationObjectIDs(t, env.storage, deleteOperationIDs, 1, outboxStatePending, []string{"password", "username"})

	env.runPeriodicAllowed("periodic delete")
	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateRemoteMissing) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateRemoteMissing)
		}
		if got := object["remote_version"]; got != "deleted" {
			t.Fatalf("%s remote version = %v, want deleted", objectID, got)
		}
	}
}

func TestAssociationSecretKeyValidation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()

	resolvedNameResp := env.update(
		"associations/app/db",
		map[string]interface{}{
			"destination":   destinationRef(providerTypeFake, "default"),
			"resolved_name": "prod/app/db/password",
			"name_template": "prod/{{ path }}/{{ key }}",
			"granularity":   syncGranularitySecretKey,
			"format":        defaultAssociationFormat,
		},
	)
	if resolvedNameResp == nil || !resolvedNameResp.IsError() {
		t.Fatalf("secret-key resolved_name response = %#v, want error", resolvedNameResp)
	}

	missingKeyResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/static",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	if missingKeyResp == nil || !missingKeyResp.IsError() {
		t.Fatalf("secret-key template without key response = %#v, want error", missingKeyResp)
	}

	kubernetesResp := env.update(
		"destinations/k8s/prod",
		map[string]interface{}{
			"description":                          "kubernetes production",
			kubernetessecrets.ConfigKeyNamespace:   "apps",
			kubernetessecrets.ConfigKeyAuthMode:    kubernetessecrets.AuthModeInCluster,
			kubernetessecrets.ConfigKeyKubeContext: "",
		},
	)
	if kubernetesResp != nil && kubernetesResp.IsError() {
		t.Fatalf("unexpected kubernetes destination write error: %v", kubernetesResp.Error())
	}
	unsupportedResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(kubernetessecrets.ProviderType, "prod"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	if unsupportedResp == nil || !unsupportedResp.IsError() {
		t.Fatalf("secret-key unsupported provider response = %#v, want error", unsupportedResp)
	}
}

func TestAssociationSecretKeyRejectsUnsupportedSourceKey(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.createFakeSecretKeyAssociation(deleteModeRetain)

	blockedResp := env.writeAppDBSecretDataNoAssert(map[string]interface{}{
		"bad/key":  "blocked",
		"password": "rotated",
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("secret-key unsupported key write response = %#v, want error", blockedResp)
	}
	readResp := env.read("data/app/db")
	assertNoErrorResponse(t, readResp)
	readMetadata := readResp.Data["metadata"].(map[string]interface{})
	if got := readMetadata["version"]; got != 1 {
		t.Fatalf("blocked secret-key write committed version = %v, want 1", got)
	}
}

func TestAssociationSecretKeyReconcileAppliesPerSourceKeyStatus(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "secret-canary",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	associationResp := env.createFakeSecretKeyAssociation(deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)

	resp := env.update("reconcile/app/db")
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "state", string(domain.SyncStateSynced))
	reconcileObjects := objectsByIDFromRaw(t, resp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := reconcileObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateSynced) {
			t.Fatalf("%s reconcile state = %v, want %s", objectID, got, domain.SyncStateSynced)
		}
		if got := object["association_id"]; got != associationID {
			t.Fatalf("%s reconcile association_id = %v, want %s", objectID, got, associationID)
		}
	}
	if strings.Contains(fmt.Sprint(resp.Data), "secret-canary") || strings.Contains(fmt.Sprint(resp.Data), "appuser") {
		t.Fatalf("secret-key reconcile response contains secret value: %#v", resp.Data)
	}

	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateSynced) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateSynced)
		}
	}
}

func TestAssociationSecretKeyDisableMarksPerSourceKeyStatus(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	associationResp := env.createFakeSecretKeyAssociation(deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)
	operationIDs := operationIDsFromResponse(t, associationResp)

	disableResp := env.update(
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	assertNoErrorResponse(t, disableResp)
	assertAssociationEnabled(t, disableResp, false)
	assertStringSet(t, canceledOperationIDsFromResponse(t, disableResp), operationIDs)

	statusResp := env.read("status/app/db")
	assertNoErrorResponse(t, statusResp)
	statusObjects := objectsByIDFromRaw(t, statusResp.Data["objects"])
	for _, objectID := range []string{"password", "username"} {
		object := statusObjects[objectID]
		if got := object["state"]; got != string(domain.SyncStateDisabled) {
			t.Fatalf("%s status state = %v, want %s", objectID, got, domain.SyncStateDisabled)
		}
	}
	if _, ok := statusObjects[syncObjectIDSecretPath]; ok {
		t.Fatalf("secret-key status must not include %s object: %#v", syncObjectIDSecretPath, statusObjects)
	}
}

func TestAssociationSecretKeyDisableSkipsStatusWhenCurrentVersionMissing(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	associationResp := env.createFakeSecretKeyAssociation(deleteModeRetain)
	associationID := associationIDFromResponse(t, associationResp)
	metadata, err := getMetadata(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if err := deleteVersion(context.Background(), env.storage, "app/db", metadata.CurrentVersion); err != nil {
		t.Fatalf("delete current version fixture: %v", err)
	}

	disableResp := env.update(
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	assertNoErrorResponse(t, disableResp)
	status, err := getStatus(context.Background(), env.storage, "app/db", associationID, syncObjectIDSecretPath)
	if err != nil {
		t.Fatalf("read secret-path status: %v", err)
	}
	if status != nil {
		t.Fatalf("secret-key disable wrote phantom status: %#v", status)
	}
}
