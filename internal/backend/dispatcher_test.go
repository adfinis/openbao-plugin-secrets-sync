package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/outbox"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/hashicorp/go-hclog"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestDispatchHonorsTightenedDestinationPolicy(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.enableAppDBSourceSync()
	writeResp := env.update(
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "prod/app/",
		},
	)
	if writeResp != nil && writeResp.IsError() {
		t.Fatalf("unexpected destination write error: %v", writeResp.Error())
	}
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "restricted"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	tightenResp := env.update(
		"destinations/fake/restricted",
		map[string]interface{}{
			destinationAllowedSourcePathPrefixesField:   "app",
			destinationAllowedResolvedNamePrefixesField: "other/",
		},
	)
	if tightenResp != nil && tightenResp.IsError() {
		t.Fatalf("unexpected destination tighten error: %v", tightenResp.Error())
	}
	env.runPeriodicAllowed("periodic after destination policy tightened")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)
}

func TestDispatchHonorsHardenedPostureEnabledAfterEnqueue(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]

	cfgResp := env.update("config", map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after hardened posture enabled")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)
}

func TestDispatchHonorsHardenedSourceOptInAfterEnqueue(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createDefaultConstrainedFakeDestination()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	cfgResp := env.update("config", map[string]interface{}{
		"security_posture": securityPostureHardened,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after hardened posture enabled")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)
}

func TestLoadDispatchTargetContextFailures(t *testing.T) {
	t.Run("upsert reports missing association", func(t *testing.T) {
		env := newBackendTestEnv(t)
		putDispatchVersionFixture(t, env.storage)

		failure := loadUpsertFailure(t, env, dispatchOutboxRecord(outbox.OperationTypeUpsert))
		assertDispatchFailure(t, failure, providers.ErrorClassInternal, "association is missing", "")
	})

	t.Run("upsert reports missing destination", func(t *testing.T) {
		env := newBackendTestEnv(t)
		putDispatchVersionFixture(t, env.storage)
		putDispatchAssociationFixture(t, env.storage, dispatchAssociationFixture())

		failure := loadUpsertFailure(t, env, dispatchOutboxRecord(outbox.OperationTypeUpsert))
		assertDispatchFailure(t, failure, providers.ErrorClassInternal, "destination is missing", "prod/app/db")
	})

	t.Run("upsert reports unsupported provider", func(t *testing.T) {
		env := newBackendTestEnv(t)
		putDispatchVersionFixture(t, env.storage)
		putDispatchAssociationFixture(t, env.storage, dispatchAssociationFixture(func(record *associationRecord) {
			record.DestinationType = "unsupported"
			record.DestinationName = "default"
			record.DestinationRef = "unsupported/default"
		}))
		putDispatchDestinationFixture(t, env.storage, destinationRecord{
			Type: "unsupported",
			Name: "default",
		})

		failure := loadUpsertFailure(t, env, dispatchOutboxRecord(outbox.OperationTypeUpsert))
		assertDispatchFailure(
			t,
			failure,
			providers.ErrorClassValidation,
			"destination provider is unsupported",
			"prod/app/db",
		)
	})

	t.Run("upsert reports disabled target", func(t *testing.T) {
		env := newBackendTestEnv(t)
		putDispatchVersionFixture(t, env.storage)
		putDispatchAssociationFixture(t, env.storage, dispatchAssociationFixture(func(record *associationRecord) {
			record.Enabled = false
		}))
		putDispatchDestinationFixture(t, env.storage, dispatchDestinationFixture())

		failure := loadUpsertFailure(t, env, dispatchOutboxRecord(outbox.OperationTypeUpsert))
		assertDispatchFailure(
			t,
			failure,
			providers.ErrorClassValidation,
			"association or destination is disabled",
			"prod/app/db",
		)
	})

	t.Run("delete mode blocks before destination lookup", func(t *testing.T) {
		env := newBackendTestEnv(t)
		putDispatchAssociationFixture(t, env.storage, dispatchAssociationFixture(func(record *associationRecord) {
			record.DeleteMode = deleteModeRetain
		}))

		failure := loadDeleteFailure(t, env, dispatchOutboxRecord(outbox.OperationTypeDelete))
		assertDispatchFailure(
			t,
			failure,
			providers.ErrorClassValidation,
			"association delete_mode does not permit remote delete",
			"prod/app/db",
		)
	})
}

func TestDriftRepairWarningLogsThresholdWithoutSecretIdentifiers(t *testing.T) {
	var logs bytes.Buffer
	b := newBackendForTest(&logical.BackendConfig{})
	if err := b.Setup(context.Background(), &logical.BackendConfig{
		Logger: hclog.New(&hclog.LoggerOptions{
			Output: &logs,
			Level:  hclog.Warn,
		}),
	}); err != nil {
		t.Fatalf("setup backend: %v", err)
	}

	b.warnIfDriftRepairThresholdExceeded(outboxRecord{
		Path:           "app/db",
		DestinationRef: "fake/default",
		ObjectID:       syncObjectIDSecretPath,
		Trigger:        outboxTriggerDriftRepair,
	}, &statusRecord{RepairCount: driftRepairWarningThreshold + 1})

	logOutput := logs.String()
	if !strings.Contains(logOutput, "background drift repair count exceeded threshold") {
		t.Fatalf("warning log = %q, want drift repair threshold warning", logOutput)
	}
	if !strings.Contains(logOutput, "destination_type=fake") {
		t.Fatalf("warning log = %q, want destination type label", logOutput)
	}
	if strings.Contains(logOutput, "app/db") {
		t.Fatalf("warning log leaked source path: %q", logOutput)
	}
}

func dispatchOutboxRecord(operationType outbox.OperationType) outboxRecord {
	return outboxRecord{
		ID:             "op-dispatch-test",
		Type:           operationType,
		Path:           "app/db",
		Version:        1,
		AssociationID:  "assoc-dispatch-test",
		ObjectID:       syncObjectIDSecretPath,
		DestinationRef: "fake/default",
	}
}

func dispatchAssociationFixture(modifiers ...func(*associationRecord)) associationRecord {
	record := associationRecord{
		ID:              "assoc-dispatch-test",
		Path:            "app/db",
		DestinationType: providerTypeFake,
		DestinationName: "default",
		DestinationRef:  "fake/default",
		ResolvedName:    "prod/app/db",
		Granularity:     syncGranularitySecretPath,
		Format:          defaultAssociationFormat,
		DeleteMode:      deleteModeDelete,
		Enabled:         true,
	}
	for _, modifier := range modifiers {
		modifier(&record)
	}
	return record
}

func dispatchDestinationFixture() destinationRecord {
	return destinationRecord{
		Type: providerTypeFake,
		Name: "default",
	}
}

func putDispatchVersionFixture(t *testing.T, storage logical.Storage) {
	t.Helper()
	if err := putVersion(context.Background(), storage, "app/db", versionRecord{
		Version: 1,
		Data: secretPayload{
			"password": "secret",
		},
	}); err != nil {
		t.Fatalf("write version fixture: %v", err)
	}
}

func putDispatchAssociationFixture(t *testing.T, storage logical.Storage, record associationRecord) {
	t.Helper()
	if err := putAssociation(context.Background(), storage, record); err != nil {
		t.Fatalf("write association fixture: %v", err)
	}
}

func putDispatchDestinationFixture(t *testing.T, storage logical.Storage, record destinationRecord) {
	t.Helper()
	if err := putDestination(context.Background(), storage, record); err != nil {
		t.Fatalf("write destination fixture: %v", err)
	}
}

func loadUpsertFailure(t *testing.T, env *backendTestEnv, record outboxRecord) *operationFailure {
	t.Helper()
	ctxData, failure, err := env.b.loadUpsertContext(context.Background(), env.storage, record)
	if err != nil {
		t.Fatalf("loadUpsertContext() error = %v", err)
	}
	if ctxData != nil {
		t.Fatalf("loadUpsertContext() context = %#v, want nil", ctxData)
	}
	if failure == nil {
		t.Fatal("loadUpsertContext() failure = nil, want failure")
	}
	return failure
}

func loadDeleteFailure(t *testing.T, env *backendTestEnv, record outboxRecord) *operationFailure {
	t.Helper()
	ctxData, failure, err := env.b.loadDeleteContext(context.Background(), env.storage, record)
	if err != nil {
		t.Fatalf("loadDeleteContext() error = %v", err)
	}
	if ctxData != nil {
		t.Fatalf("loadDeleteContext() context = %#v, want nil", ctxData)
	}
	if failure == nil {
		t.Fatal("loadDeleteContext() failure = nil, want failure")
	}
	return failure
}

func assertDispatchFailure(
	t *testing.T,
	failure *operationFailure,
	wantClass providers.ErrorClass,
	wantMessage string,
	wantResolvedName string,
) {
	t.Helper()
	if failure.class != wantClass {
		t.Fatalf("failure class = %s, want %s", failure.class, wantClass)
	}
	if failure.message != wantMessage {
		t.Fatalf("failure message = %q, want %q", failure.message, wantMessage)
	}
	if failure.resolvedName != wantResolvedName {
		t.Fatalf("failure resolvedName = %q, want %q", failure.resolvedName, wantResolvedName)
	}
}
