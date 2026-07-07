package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/hashicorp/go-hclog"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestDispatchHonorsTightenedDestinationPolicy(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.markAppDBSyncable()
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

func TestDispatchHonorsDelegatedModeEnabledAfterEnqueue(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.createDefaultFakeAssociation()
	operationID := operationIDsFromResponse(t, associationResp)[0]

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
		"delegated_mode":        true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after delegated mode enabled")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)
}

func TestDispatchHonorsTightenedSourceOptInPolicy(t *testing.T) {
	env := newBackendTestEnv(t)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"granularity":   syncObjectIDSecretPath,
		"format":        defaultAssociationFormat,
	})
	operationID := operationIDsFromResponse(t, associationResp)[0]

	cfgResp := env.update("config", map[string]interface{}{
		"require_source_opt_in": true,
	})
	if cfgResp != nil && cfgResp.IsError() {
		t.Fatalf("unexpected config write error: %v", cfgResp.Error())
	}
	env.runPeriodicAllowed("periodic after source opt-in policy tightened")
	assertOutboxOperation(t, env.storage, operationID, 1, outboxStateFailedTerminal)
	assertStatusObjectErrorClass(t, env.b, env.storage, providers.ErrorClassValidation)
	assertStatusObjectState(t, env.b, env.storage, domain.SyncStateValidationError)
}

func TestDriftRepairWarningLogsThresholdWithoutSecretIdentifiers(t *testing.T) {
	var logs bytes.Buffer
	b := Backend(&logical.BackendConfig{})
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
