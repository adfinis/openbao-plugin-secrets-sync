package backend

import (
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
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
		"destination_type": providerTypeFake,
		"destination_name": "restricted",
		"resolved_name":    "prod/app/db",
		"granularity":      syncObjectIDSecretPath,
		"format":           defaultAssociationFormat,
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
