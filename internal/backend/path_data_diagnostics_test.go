package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/domain"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestDataWriteReportsSourceSyncEligibilityBlocker(t *testing.T) {
	for _, previouslyEnabled := range []bool{false, true} {
		name := "never-enabled"
		if previouslyEnabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			env := newBackendTestEnv(t)
			env.writeAppDBSecret("initial")
			env.createDefaultConstrainedFakeDestination()
			if previouslyEnabled {
				env.enableAppDBSourceSync()
			}
			assertNoErrorResponse(t, env.update("associations/app/db", map[string]interface{}{
				"destination":   destinationRef(providerTypeFake, "default"),
				"resolved_name": "prod/app/db",
			}))
			env.runPeriodicAllowed("initial source sync")
			if previouslyEnabled {
				assertNoErrorResponse(t, env.update("sources/app/db/disable"))
			}
			if resp := env.update("config", map[string]interface{}{
				"security_posture": securityPostureHardened,
			}); resp != nil && resp.IsError() {
				t.Fatalf("configure hardened posture: %v", resp.Error())
			}
			assertQueueCount(t, env.b, env.storage, "pending", 0)

			resp, err := env.b.HandleRequest(context.Background(), &logical.Request{
				Operation:  logical.UpdateOperation,
				Path:       "data/app/db",
				MountPoint: "nested/secret-sync/",
				Storage:    env.storage,
				Data: map[string]interface{}{
					"data": map[string]interface{}{"password": "rotated"},
				},
			})
			if err != nil {
				t.Fatalf("write source: %v", err)
			}
			assertNoErrorResponse(t, resp)
			metadata := resp.Data["metadata"].(map[string]interface{})
			assertResponseValue(t, &logical.Response{Data: metadata}, "version", 2)
			assertResponseValue(t, &logical.Response{Data: metadata}, "sync_state", string(domain.SyncStateValidationError))
			assertOperationIDs(t, metadata, 0)
			assertHintContains(t, metadata, "Source version was stored, but sync work was not enqueued")
			assertHintContains(t, metadata, "Source sync must be enabled")
			assertNextActionCommand(t, metadata, "enable_source", "bao write -force nested/secret-sync/sources/app/db/enable")
			assertQueueCount(t, env.b, env.storage, "pending", 0)

			stored := env.read("data/app/db")
			assertNoErrorResponse(t, stored)
			if got := stored.Data["data"].(secretPayload)["password"]; got != "rotated" {
				t.Fatalf("stored password = %v, want rotated", got)
			}
			storedMetadata := stored.Data["metadata"].(map[string]interface{})
			assertResponseValue(t, &logical.Response{Data: storedMetadata}, "version", 2)
			assertResponseValue(t, env.read("sources/app/db/check"), "source_sync_enabled", false)

			enable := env.update("sources/app/db/enable")
			assertNoErrorResponse(t, enable)
			assertResponseValue(t, enable, "sync_state", string(domain.SyncStatePending))
			ids := operationIDsFromResponse(t, enable)
			operationID := requireSingleOperationID(t, ids, "enable source")
			assertOutboxOperation(t, env.storage, operationID, 2, outboxStatePending)
		})
	}
}

func TestDataWriteWithoutActiveAssociationsPreservesNoAssociationState(t *testing.T) {
	for _, disabledObject := range []string{"none", "association", "destination"} {
		t.Run(disabledObject, func(t *testing.T) {
			env := newBackendTestEnv(t)
			env.writeAppDBSecret("initial")
			if disabledObject != "none" {
				env.createDefaultConstrainedFakeDestination()
				association := env.createDefaultFakeAssociation()
				env.runPeriodicAllowed("initial source sync")
				assertNoErrorResponse(t, env.update("sources/app/db/disable"))
				if disabledObject == "association" {
					assertNoErrorResponse(t, env.update("associations/app/db/"+associationIDFromResponse(t, association)+"/disable"))
				} else {
					resp := env.update("destinations/fake/default", map[string]interface{}{"disabled": true})
					if resp != nil && resp.IsError() {
						t.Fatalf("disable destination: %v", resp.Error())
					}
				}
			}
			if resp := env.update("config", map[string]interface{}{
				"security_posture": securityPostureHardened,
			}); resp != nil && resp.IsError() {
				t.Fatalf("configure hardened posture: %v", resp.Error())
			}
			resp := env.writeAppDBSecret("rotated")
			metadata := resp.Data["metadata"].(map[string]interface{})
			assertResponseValue(t, &logical.Response{Data: metadata}, "sync_state", string(domain.SyncStateNoAssociation))
			assertOperationIDs(t, metadata, 0)
			for _, field := range []string{"hint", "next_actions"} {
				if _, ok := metadata[field]; ok {
					t.Fatalf("metadata contains %s without active associations", field)
				}
			}
			assertQueueCount(t, env.b, env.storage, "pending", 0)
		})
	}
}
