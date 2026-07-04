package backend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/observability"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestAssociationCreateUsesSafeDefaults(t *testing.T) {
	env := newBackendTestEnv(t)
	env.createFakeDestination("default")
	env.writeAppDBSecret("secret")

	enableResp := env.update("sources/app/db/enable")
	assertNoErrorResponse(t, enableResp)

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
	})
	assertNoErrorResponse(t, resp)
	assertResponseValue(t, resp, "resolved_name", "app/db")
	assertResponseValue(t, resp, "granularity", syncGranularitySecretPath)
	assertResponseValue(t, resp, "format", defaultAssociationFormat)
	assertResponseValue(t, resp, "delete_mode", deleteModeRetain)
	assertResponseValue(t, resp, "enabled", true)
	assertNoDefaultsInResponse(t, resp)
	operationIDs := operationIDsFromResponse(t, resp)
	if len(operationIDs) != 1 {
		t.Fatalf("sync_operation_ids = %v, want one operation", operationIDs)
	}
}

func TestAssociationListPagination(t *testing.T) {
	env := newBackendTestEnv(t)

	env.createFakeDestination("default")
	for _, path := range []string{"app/db", "shared/db", "team/db"} {
		env.createFakeAssociationForPath(path)
	}

	assertListKeys(t,
		env.list("associations", map[string]interface{}{
			"limit": 2,
		}),
		[]string{"app/", "shared/"},
	)
	assertListKeys(t,
		env.list("associations", map[string]interface{}{
			"after": "app/",
			"limit": 1,
		}),
		[]string{"shared/"},
	)
	assertListKeys(t,
		env.list("associations", map[string]interface{}{
			"after": "b",
			"limit": 1,
		}),
		[]string{"shared/"},
	)
}

func TestAssociationCreateQueuesCurrentVersion(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationIDs := operationIDsFromResponse(t, associationResp)
	if len(operationIDs) != 1 {
		t.Fatalf("association operation IDs = %v, want one operation", operationIDs)
	}

	queueResp := env.read("queue")
	assertNoErrorResponse(t, queueResp)
	assertResponseValue(t, queueResp, "pending", 1)

	readResp := env.read("associations/app/db")
	assertNoErrorResponse(t, readResp)
	associations := readResp.Data["associations"].([]map[string]interface{})
	if len(associations) != 1 {
		t.Fatalf("associations length = %d, want 1", len(associations))
	}
	if got := associations[0]["resolved_name"]; got != "prod/app/db" {
		t.Fatalf("resolved name = %v, want prod/app/db", got)
	}
}

func TestAssociationCreateQueueCapacityFailureDoesNotPersistAssociation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	configResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 0,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}

	resp := env.createDefaultFakeAssociation()
	if resp == nil || !resp.IsError() {
		t.Fatalf("association create response = %#v, want queue capacity error", resp)
	}
	assertHintContains(t, resp.Data, "Queue capacity is exhausted")
	assertAssociationCount(t, env.storage, "app/db", 0)
	assertQueueCount(t, env.b, env.storage, "pending", 0)
}

func TestAssociationCreateAndPlanUseDestinationRef(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")

	planResp := env.update("associations/app/db/plan", map[string]interface{}{
		"destination":   "fake/default",
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, planResp)
	assertResponseValue(t, planResp, "destination_ref", "fake/default")

	createResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   "fake/default",
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, createResp)
	assertResponseValue(t, createResp, "destination_ref", "fake/default")
	associationID := associationIDFromResponse(t, createResp)

	readResp := env.read("associations/app/db/" + associationID)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "id", associationID)
	assertResponseValue(t, readResp, "destination_ref", "fake/default")
}

func TestAssociationResponsesOmitStaticDefaults(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")

	planResp := env.update("associations/app/db/plan", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoDefaultsInResponse(t, planResp)

	createResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoDefaultsInResponse(t, createResp)
	associationID := associationIDFromResponse(t, createResp)

	readByIDResp := env.read("associations/app/db/" + associationID)
	assertNoDefaultsInResponse(t, readByIDResp)

	readListResp := env.read("associations/app/db")
	assertNoDefaultsInResponse(t, readListResp)

	enableResp := env.update("associations/app/db/"+associationID+"/enable", nil)
	assertNoDefaultsInResponse(t, enableResp)
}

func TestAssociationRejectsInvalidDestination(t *testing.T) {
	env := newBackendTestEnv(t)

	missingResp := env.update("associations/app/db", map[string]interface{}{
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	if missingResp == nil || !missingResp.IsError() {
		t.Fatalf("missing destination response = %#v, want error", missingResp)
	}
	if !strings.Contains(missingResp.Error().Error(), "destination is required") {
		t.Fatalf("missing destination error = %q, want required", missingResp.Error().Error())
	}

	malformedResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   "fake",
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	if malformedResp == nil || !malformedResp.IsError() {
		t.Fatalf("malformed destination response = %#v, want error", malformedResp)
	}
	if !strings.Contains(malformedResp.Error().Error(), "destination must be in <type>/<name> form") {
		t.Fatalf("malformed destination error = %q, want form error", malformedResp.Error().Error())
	}
}

func assertNoDefaultsInResponse(t *testing.T, resp *logical.Response) {
	t.Helper()
	assertNoErrorResponse(t, resp)
	assertNoDefaultsInValue(t, resp.Data, "response")
}

func assertNoDefaultsInValue(t *testing.T, value interface{}, path string) { //nolint:forbidigo
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		if _, ok := typed["defaults"]; ok {
			t.Fatalf("%s contains defaults: %#v", path, typed)
		}
		for key, child := range typed {
			assertNoDefaultsInValue(t, child, path+"."+key)
		}
	case []map[string]interface{}:
		for index, child := range typed {
			assertNoDefaultsInValue(t, child, fmt.Sprintf("%s[%d]", path, index))
		}
	case []interface{}:
		for index, child := range typed {
			assertNoDefaultsInValue(t, child, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}

func TestAssociationUpdateMergesOmittedFieldsFromExistingRecord(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, initialResp)
	associationID := associationIDFromResponse(t, initialResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
		"delete_mode": deleteModeDelete,
	})
	assertNoErrorResponse(t, updateResp)
	updateAssociationID := associationIDFromResponse(t, updateResp)
	if updateAssociationID != associationID {
		t.Fatalf("updated association ID = %s, want %s", updateAssociationID, associationID)
	}
	if operationIDs := operationIDsFromResponse(t, updateResp); len(operationIDs) != 0 {
		t.Fatalf("update operation IDs = %v, want none", operationIDs)
	}

	records, err := listAssociationsForPath(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("list associations: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("association count = %d, want 1", len(records))
	}
	record := records[0]
	if got := record.Granularity; got != syncGranularitySecretKey {
		t.Fatalf("granularity = %s, want %s", got, syncGranularitySecretKey)
	}
	if got := record.NameTemplate; got != "prod/{{ path }}/{{ key }}" {
		t.Fatalf("name_template = %s, want original template", got)
	}
	if record.Enabled {
		t.Fatal("association should remain disabled when enabled is omitted")
	}
	if got := record.DeleteMode; got != deleteModeDelete {
		t.Fatalf("delete_mode = %s, want %s", got, deleteModeDelete)
	}
}

func TestAssociationUpdateRejectsGranularityIdentityChange(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncGranularitySecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, initialResp)
	associationID := associationIDFromResponse(t, initialResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertAssociationIdentityGuardError(t, updateResp, "granularity", associationID)
	assertStoredAssociationIdentity(t, env.storage, associationID, syncGranularitySecretPath, "prod/app/db")

	enableResp := env.update("associations/app/db/"+associationID+"/enable", nil)
	assertNoErrorResponse(t, enableResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "enable")
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if operation.AssociationID != associationID {
		t.Fatalf("operation association ID = %s, want %s", operation.AssociationID, associationID)
	}

	explicitCreateResp := env.handle(logical.CreateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, explicitCreateResp)
	explicitAssociationID := associationIDFromResponse(t, explicitCreateResp)
	if explicitAssociationID == associationID {
		t.Fatalf("explicit create association ID = %s, want a distinct ID", explicitAssociationID)
	}
	assertAssociationCount(t, env.storage, "app/db", 2)
}

func TestAssociationUpdateRejectsReservationIdentityChange(t *testing.T) {
	tests := []struct {
		name            string
		initialRequest  map[string]interface{}
		updateRequest   map[string]interface{}
		field           string
		granularity     string
		reservationName string
	}{
		{
			name: "secret path resolved name",
			initialRequest: map[string]interface{}{
				"destination":   destinationRef(providerTypeFake, "default"),
				"resolved_name": "prod/app/db",
				"granularity":   syncGranularitySecretPath,
				"format":        defaultAssociationFormat,
				"enabled":       false,
			},
			updateRequest: map[string]interface{}{
				"destination":   destinationRef(providerTypeFake, "default"),
				"resolved_name": "prod/app/renamed",
			},
			field:           "resolved_name",
			granularity:     syncGranularitySecretPath,
			reservationName: "prod/app/db",
		},
		{
			name: "secret key name template",
			initialRequest: map[string]interface{}{
				"destination":   destinationRef(providerTypeFake, "default"),
				"name_template": "prod/{{ path }}/{{ key }}",
				"granularity":   syncGranularitySecretKey,
				"format":        defaultAssociationFormat,
				"enabled":       false,
			},
			updateRequest: map[string]interface{}{
				"destination":   destinationRef(providerTypeFake, "default"),
				"name_template": "prod/{{ path }}/renamed/{{ key }}",
			},
			field:           "name_template",
			granularity:     syncGranularitySecretKey,
			reservationName: "prod/app/db/${key}",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			env := newBackendTestEnv(t)
			env.writeAppDBSecretData(map[string]interface{}{
				"password": "initial",
			})
			env.createFakeDestination("default")
			env.markAppDBSyncable()
			initialResp := env.update("associations/app/db", testCase.initialRequest)
			assertNoErrorResponse(t, initialResp)
			associationID := associationIDFromResponse(t, initialResp)

			updateResp := env.update("associations/app/db", testCase.updateRequest)
			assertAssociationIdentityGuardError(t, updateResp, testCase.field, associationID)
			assertStoredAssociationIdentity(
				t,
				env.storage,
				associationID,
				testCase.granularity,
				testCase.reservationName,
			)
		})
	}
}

func assertAssociationIdentityGuardError(
	t *testing.T,
	resp *logical.Response,
	field string,
	associationID string,
) {
	t.Helper()
	if resp == nil || !resp.IsError() {
		t.Fatalf("identity guard response = %#v, want error", resp)
	}
	message := resp.Error().Error()
	for _, want := range []string{
		field + " change would create a new association identity",
		"delete " + associationID + " first",
		"create the new association and delete the old one explicitly",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("identity guard error = %q, want substring %q", message, want)
		}
	}
}

func assertStoredAssociationIdentity(
	t *testing.T,
	storage logical.Storage,
	associationID string,
	granularity string,
	reservationName string,
) {
	t.Helper()
	assertAssociationCount(t, storage, "app/db", 1)
	record, err := getAssociation(context.Background(), storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if record == nil {
		t.Fatalf("association %s must exist", associationID)
	}
	if record.Granularity != granularity {
		t.Fatalf("association granularity = %s, want %s", record.Granularity, granularity)
	}
	if got := record.reservationName(); got != reservationName {
		t.Fatalf("association reservation = %s, want %s", got, reservationName)
	}
}

func assertAssociationCount(t *testing.T, storage logical.Storage, path string, want int) {
	t.Helper()
	records, err := listAssociationsForPath(context.Background(), storage, path)
	if err != nil {
		t.Fatalf("list associations: %v", err)
	}
	if len(records) != want {
		t.Fatalf("association count = %d, want %d", len(records), want)
	}
}

func TestAssociationUpdateEnabledRecordReturnsManualSyncHint(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	initialResp := env.createDefaultFakeAssociation()
	assertNoErrorResponse(t, initialResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
		"delete_mode": deleteModeDelete,
	})
	assertNoErrorResponse(t, updateResp)
	if operationIDs := operationIDsFromResponse(t, updateResp); len(operationIDs) != 0 {
		t.Fatalf("update operation IDs = %v, want none", operationIDs)
	}
	assertHintContains(t, updateResp.Data, "did not enqueue sync work")
	assertNextActionCommand(
		t,
		updateResp.Data,
		"manual_sync",
		"bao write <mount>/associations/app/db/sync destination=fake/default",
	)
}

func TestAssociationUpdateEnqueuesWhenEnablingExistingRecord(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncGranularitySecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, initialResp)
	if operationIDs := operationIDsFromResponse(t, initialResp); len(operationIDs) != 0 {
		t.Fatalf("initial operation IDs = %v, want none", operationIDs)
	}

	enableResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
		"enabled":     true,
	})
	assertNoErrorResponse(t, enableResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "enable through write")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func TestAssociationEnableQueueCapacityFailureLeavesAssociationDisabled(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncGranularitySecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, initialResp)
	if operationIDs := operationIDsFromResponse(t, initialResp); len(operationIDs) != 0 {
		t.Fatalf("initial operation IDs = %v, want none", operationIDs)
	}
	associationID := associationIDFromResponse(t, initialResp)
	configResp := env.update(configPath, map[string]interface{}{
		"queue_capacity": 0,
	})
	if configResp != nil && configResp.IsError() {
		t.Fatalf("unexpected config write error: %v", configResp.Error())
	}

	enableResp := env.update("associations/app/db/" + associationID + "/enable")
	if enableResp == nil || !enableResp.IsError() {
		t.Fatalf("association enable response = %#v, want queue capacity error", enableResp)
	}
	assertHintContains(t, enableResp.Data, "Queue capacity is exhausted")

	readResp := env.read("associations/app/db/" + associationID)
	assertNoErrorResponse(t, readResp)
	assertResponseValue(t, readResp, "enabled", false)
	assertQueueCount(t, env.b, env.storage, "pending", 0)
}

func TestAssociationPlanMergesOmittedFieldsFromExistingRecord(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	initialResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, initialResp)

	planResp := env.update("associations/app/db/plan", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
		"delete_mode": deleteModeDelete,
	})
	assertNoErrorResponse(t, planResp)
	assertResponseValue(t, planResp, "association_id", associationIDFromResponse(t, initialResp))
	assertResponseValue(t, planResp, "granularity", syncGranularitySecretKey)
	objects := objectsByIDFromRaw(t, planResp.Data["objects"])
	if _, ok := objects["password"]; !ok {
		t.Fatalf("plan objects = %#v, want password object", objects)
	}
}

func TestAssociationUpdateRejectsAmbiguousDestinationBase(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	env.markAppDBSyncable()
	firstResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncGranularitySecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, firstResp)
	secondResp := env.handle(logical.CreateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, secondResp)

	updateResp := env.update("associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
		"delete_mode": deleteModeDelete,
	})
	if updateResp == nil || !updateResp.IsError() {
		t.Fatalf("ambiguous update response = %#v, want error", updateResp)
	}
	if !strings.Contains(updateResp.Error().Error(), "ambiguous") {
		t.Fatalf("ambiguous update error = %q, want ambiguity", updateResp.Error().Error())
	}
	records, err := listAssociationsForPath(context.Background(), env.storage, "app/db")
	if err != nil {
		t.Fatalf("list associations: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("association count = %d, want 2", len(records))
	}
}

func TestAssociationDisableRejectsClaimedOperation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	claimOperationFixture(t, env.storage, operationID)

	disableResp := env.update(
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	if disableResp == nil || !disableResp.IsError() {
		t.Fatalf("disable claimed operation response = %#v, want error", disableResp)
	}
	association, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if association == nil || !association.Enabled {
		t.Fatalf("association after failed disable = %#v, want enabled", association)
	}
	operation := assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
	if operation.ClaimOwner == "" {
		t.Fatal("operation claim must remain active")
	}
}

func TestAssociationDeleteRejectsClaimedOperation(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	operationID := requireSingleOperationID(t, operationIDsFromResponse(t, associationResp), "association")
	claimOperationFixture(t, env.storage, operationID)

	deleteResp := env.delete(
		"associations/app/db/"+associationID,
		nil,
	)
	if deleteResp == nil || !deleteResp.IsError() {
		t.Fatalf("delete claimed operation response = %#v, want error", deleteResp)
	}
	association, err := getAssociation(context.Background(), env.storage, "app/db", associationID)
	if err != nil {
		t.Fatalf("read association: %v", err)
	}
	if association == nil {
		t.Fatal("association must remain after failed delete")
	}
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStatePending)
}

func TestOperationMetricsUseGranularityLabels(t *testing.T) {
	b := Backend(&logical.BackendConfig{})
	recorder := &recordingObserver{}
	b.observer = recorder
	storage := &logical.InmemStorage{}

	writeAppDBSecretData(t, b, storage, map[string]interface{}{
		"password": "initial",
		"username": "appuser",
	})
	createFakeDestination(t, b, storage, "default")
	createFakeSecretKeyAssociation(t, b, storage, deleteModeRetain)

	runPeriodicAllowed(t, b, storage, "periodic")

	successGranularities := map[string]struct{}{}
	for _, event := range recorder.operations {
		if event.Operation != observability.OperationUpsert || event.Result != observability.ResultSuccess {
			continue
		}
		successGranularities[event.Granularity] = struct{}{}
		if event.Granularity == "password" || event.Granularity == "username" {
			t.Fatalf("operation metric leaked source key granularity: %#v", recorder.operations)
		}
	}
	if _, ok := successGranularities[syncGranularitySecretKey]; !ok {
		t.Fatalf("operation granularities = %v, want %s", successGranularities, syncGranularitySecretKey)
	}
}

func TestAssociationRequiresSyncableMetadata(t *testing.T) {
	env := newBackendTestEnv(t)

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")

	blockedResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	if blockedResp == nil || !blockedResp.IsError() {
		t.Fatalf("association without syncable metadata response = %#v, want error", blockedResp)
	}

	env.markAppDBSyncable()
	allowedResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, allowedResp)
}

func TestAssociationAllowsNonSyncableSourceByDefault(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")

	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, resp)
	assertOperationIDs(t, resp.Data, 1)
}

func TestAssociationDestinationPolicyConstraints(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.markAppDBSyncable()
	writeResp := env.update(
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "team/",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}

	sourceBlockedResp := env.update(
		"associations/app/db",
		map[string]interface{}{
			"destination":   destinationRef(providerTypeFake, "restricted"),
			"resolved_name": "prod/app/db",
			"granularity":   syncObjectIDSecretPath,
			"format":        defaultAssociationFormat,
		},
	)
	if sourceBlockedResp == nil || !sourceBlockedResp.IsError() {
		t.Fatalf("source policy response = %#v, want error", sourceBlockedResp)
	}
	if !strings.Contains(sourceBlockedResp.Error().Error(), "does not allow source path") {
		t.Fatalf("source policy error = %q", sourceBlockedResp.Error().Error())
	}

	updateResp := env.update(
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if updateResp != nil && updateResp.IsError() {
		t.Fatalf("unexpected destination update error: %v", updateResp.Error())
	}
	nameBlockedPlan := env.update(
		"associations/app/db/plan",
		map[string]interface{}{
			"destination":   destinationRef(providerTypeFake, "restricted"),
			"resolved_name": "prod/other/db",
			"granularity":   syncObjectIDSecretPath,
			"format":        defaultAssociationFormat,
		},
	)
	assertNoErrorResponse(t, nameBlockedPlan)
	assertResponseValue(t, nameBlockedPlan, "source_eligible", true)
	assertResponseValue(t, nameBlockedPlan, "action", providers.PlanActionBlocked)
	assertResponseValue(t, nameBlockedPlan, "error_class", string(providers.ErrorClassValidation))
	if !strings.Contains(nameBlockedPlan.Data["message"].(string), "does not allow resolved name") {
		t.Fatalf("name policy message = %q", nameBlockedPlan.Data["message"])
	}

	nameBlockedWrite := env.update(
		"associations/app/db",
		map[string]interface{}{
			"destination":   destinationRef(providerTypeFake, "restricted"),
			"resolved_name": "prod/other/db",
			"granularity":   syncObjectIDSecretPath,
			"format":        defaultAssociationFormat,
		},
	)
	if nameBlockedWrite == nil || !nameBlockedWrite.IsError() {
		t.Fatalf("name policy write response = %#v, want error", nameBlockedWrite)
	}

	allowedResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "restricted"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	assertNoErrorResponse(t, allowedResp)
}

func TestAssociationPlan(t *testing.T) {
	env := newBackendTestEnv(t)

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")

	blockedResp := env.planDefaultFakeAssociation("prod/app/db")
	assertNoErrorResponse(t, blockedResp)
	assertResponseValue(t, blockedResp, "action", providers.PlanActionBlocked)
	assertResponseValue(t, blockedResp, "source_eligible", false)
	assertResponseValue(t, blockedResp, "error_class", string(providers.ErrorClassValidation))

	env.markAppDBSyncable()
	createResp := env.planDefaultFakeAssociation("prod/app/db")
	assertNoErrorResponse(t, createResp)
	assertResponseValue(t, createResp, "action", providers.PlanActionCreate)
	assertResponseValue(t, createResp, "source_eligible", true)
	assertNoPayloadHash(t, createResp.Data)
	if got := createResp.Data["payload_bytes"].(int); got <= 0 {
		t.Fatalf("payload_bytes = %d, want positive", got)
	}
	if strings.Contains(fmt.Sprint(createResp.Data), "initial") {
		t.Fatalf("plan response contains secret value: %#v", createResp.Data)
	}

	conflictResp := env.planDefaultFakeAssociation("prod/conflict/app/db")
	assertNoErrorResponse(t, conflictResp)
	assertResponseValue(t, conflictResp, "action", providers.PlanActionConflict)
	assertResponseValue(t, conflictResp, "error_class", string(providers.ErrorClassCollision))
}

func TestAssociationDisableEnableAndManualSync(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	associationID := associationIDFromResponse(t, associationResp)
	operationID := operationIDsFromResponse(t, associationResp)[0]

	disableResp := env.update(
		"associations/app/db/"+associationID+"/disable",
		nil,
	)
	assertNoErrorResponse(t, disableResp)
	assertAssociationEnabled(t, disableResp, false)
	assertStringSlice(t, canceledOperationIDsFromResponse(t, disableResp), []string{operationID})
	assertOutboxMissing(t, env.storage, operationID)
	assertDisabledStatusObject(t, env.b, env.storage, 1)

	disabledSyncResp := env.update(
		"associations/app/db/"+associationID+"/sync",
		nil,
	)
	if disabledSyncResp == nil || !disabledSyncResp.IsError() {
		t.Fatalf("sync disabled association response = %#v, want error", disabledSyncResp)
	}
	assertHintContains(t, disabledSyncResp.Data, "Association is disabled")
	assertNextActionCommand(
		t,
		disabledSyncResp.Data,
		"enable_association",
		"bao write <mount>/associations/app/db/enable destination=fake/default",
	)

	secondResp := env.writeAppDBSecret("rotated")
	secondMetadata := secondResp.Data["metadata"].(map[string]interface{})
	assertOperationIDs(t, secondMetadata, 0)

	enableResp := env.update(
		"associations/app/db/"+associationID+"/enable",
		nil,
	)
	assertNoErrorResponse(t, enableResp)
	assertAssociationEnabled(t, enableResp, true)
	enableOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "enable")
	enableOperation := assertOutboxOperation(t, env.storage, enableOperationID, 2, outboxStatePending)
	enableIdempotencyKey := enableOperation.IdempotencyKey
	env.runPeriodicAllowed("periodic")

	syncResp := env.update(
		"associations/app/db/"+associationID+"/sync",
		nil,
	)
	assertNoErrorResponse(t, syncResp)
	syncOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, syncResp), "manual sync")
	if syncOperationID == enableOperationID {
		t.Fatalf("manual sync operation ID reused previous operation %s", syncOperationID)
	}
	manualSyncOperation := assertOutboxOperation(t, env.storage, syncOperationID, 2, outboxStatePending)
	if manualSyncOperation.IdempotencyKey == "" {
		t.Fatal("manual sync idempotency key must be set")
	}
	if manualSyncOperation.IdempotencyKey == enableIdempotencyKey {
		t.Fatal("manual sync idempotency key must not reuse the previous operation")
	}
}

func TestAssociationDestinationAddressedLifecycle(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]

	disableResp := env.update(
		"associations/app/db/disable",
		map[string]interface{}{
			"destination": "fake/default",
		},
	)
	assertNoErrorResponse(t, disableResp)
	assertAssociationEnabled(t, disableResp, false)
	assertStringSlice(t, canceledOperationIDsFromResponse(t, disableResp), []string{operationID})
	assertOutboxMissing(t, env.storage, operationID)
	assertDisabledStatusObject(t, env.b, env.storage, 1)

	disabledSyncResp := env.update(
		"associations/app/db/sync",
		map[string]interface{}{
			"destination": destinationRef(providerTypeFake, "default"),
		},
	)
	if disabledSyncResp == nil || !disabledSyncResp.IsError() {
		t.Fatalf("sync disabled association response = %#v, want error", disabledSyncResp)
	}

	secondResp := env.writeAppDBSecret("rotated")
	secondMetadata := secondResp.Data["metadata"].(map[string]interface{})
	assertOperationIDs(t, secondMetadata, 0)

	enableResp := env.update(
		"associations/app/db/enable",
		map[string]interface{}{
			"destination": destinationRef(providerTypeFake, "default"),
		},
	)
	assertNoErrorResponse(t, enableResp)
	assertAssociationEnabled(t, enableResp, true)
	enableOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, enableResp), "enable")
	enableOperation := assertOutboxOperation(t, env.storage, enableOperationID, 2, outboxStatePending)
	enableIdempotencyKey := enableOperation.IdempotencyKey
	env.runPeriodicAllowed("periodic")

	syncResp := env.update(
		"associations/app/db/sync",
		map[string]interface{}{
			"destination": "fake/default",
		},
	)
	assertNoErrorResponse(t, syncResp)
	syncOperationID := requireSingleOperationID(t, operationIDsFromResponse(t, syncResp), "manual sync")
	if syncOperationID == enableOperationID {
		t.Fatalf("manual sync operation ID reused previous operation %s", syncOperationID)
	}
	manualSyncOperation := assertOutboxOperation(t, env.storage, syncOperationID, 2, outboxStatePending)
	if manualSyncOperation.IdempotencyKey == "" {
		t.Fatal("manual sync idempotency key must be set")
	}
	if manualSyncOperation.IdempotencyKey == enableIdempotencyKey {
		t.Fatal("manual sync idempotency key must not reuse the previous operation")
	}
}

func TestAssociationDestinationAddressedLifecycleRejectsAmbiguousDestination(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecretData(map[string]interface{}{
		"password": "initial",
	})
	env.createFakeDestination("default")
	assertNoErrorResponse(t, env.createDefaultFakeAssociation())
	secondResp := env.handle(logical.CreateOperation, "associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"name_template": "prod/{{ path }}/{{ key }}",
		"granularity":   syncGranularitySecretKey,
		"format":        defaultAssociationFormat,
		"delete_mode":   deleteModeRetain,
	})
	assertNoErrorResponse(t, secondResp)

	disableResp := env.update(
		"associations/app/db/disable",
		map[string]interface{}{
			"destination": "fake/default",
		},
	)
	if disableResp == nil || !disableResp.IsError() {
		t.Fatalf("ambiguous lifecycle response = %#v, want error", disableResp)
	}
	if !strings.Contains(disableResp.Error().Error(), "ambiguous") {
		t.Fatalf("ambiguous lifecycle error = %q, want ambiguity", disableResp.Error().Error())
	}
}

func TestAssociationEnableRequiresSyncableMetadata(t *testing.T) {
	env := newBackendTestEnv(t)

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	resp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
		"enabled":       false,
	})
	assertNoErrorResponse(t, resp)
	associationID := associationIDFromResponse(t, resp)

	enableResp := env.update(
		"associations/app/db/"+associationID+"/enable",
		nil,
	)
	if enableResp == nil || !enableResp.IsError() {
		t.Fatalf("enable without syncable metadata response = %#v, want error", enableResp)
	}
}

func TestConcurrentAssociationWritesReserveResolvedNameOnce(t *testing.T) {
	env := newBackendTestEnv(t)

	for _, path := range []string{"app/db", "app/api"} {
		resp := env.update("data/"+path, map[string]interface{}{
			"data": map[string]interface{}{
				"password": path,
			},
		})
		assertNoErrorResponse(t, resp)
		resp = env.update("metadata/"+path, map[string]interface{}{
			"custom_metadata": map[string]interface{}{
				sourceMetadataKeySyncable: sourceMetadataValueTrue,
			},
		})
		assertNoErrorResponse(t, resp)
	}
	env.createFakeDestination("default")

	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, path := range []string{"app/db", "app/api"} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation: logical.UpdateOperation,
				Path:      "associations/" + path,
				Storage:   env.storage,
				Data: map[string]interface{}{
					"destination":   destinationRef(providerTypeFake, "default"),
					"resolved_name": "prod/shared",
					"granularity":   syncObjectIDSecretPath,
					"format":        defaultAssociationFormat,
				},
			})
			if err != nil {
				t.Errorf("association write %s: %v", path, err)
				results <- false
				return
			}
			results <- resp != nil && !resp.IsError()
		}(path)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for success := range results {
		if success {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful association writes = %d, want 1", successes)
	}
	reservations, err := listAssociationNameReservationIDs(
		context.Background(),
		env.storage,
		"fake/default",
		"prod/shared",
	)
	if err != nil {
		t.Fatalf("list reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservation count = %d, want 1", len(reservations))
	}
}
