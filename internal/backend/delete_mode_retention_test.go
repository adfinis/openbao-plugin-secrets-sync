package backend

import (
	"context"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers"
	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/fake"
)

func TestRetainAndOrphanPreserveRemoteOwnership(t *testing.T) {
	for _, mode := range []string{deleteModeRetain, deleteModeOrphan} {
		for _, target := range []string{"source", "association"} {
			t.Run(mode+"/"+target, func(t *testing.T) {
				env := newBackendTestEnv(t)
				provider := &retentionTestProvider{}
				env.b.providerRegistry = providers.MustNewRegistry(provider)
				env.writeAppDBSecret("initial")
				env.createFakeDestination("default")
				association := env.update("associations/app/db", map[string]interface{}{
					"destination":   destinationRef(providerTypeFake, "default"),
					"resolved_name": "prod/app/db",
					"delete_mode":   mode,
				})
				assertNoErrorResponse(t, association)
				assertResponseValue(t, association, "delete_mode", mode)
				env.runPeriodicAllowed("initial upsert")
				owner := provider.remote.owner
				if !owner.Complete() || provider.remote.payloadSHA256 == "" || provider.remote.upserts != 1 {
					t.Fatalf("initial remote = %#v, want one owned upsert", provider.remote)
				}
				before := provider.remote

				if target == "source" {
					resp := env.delete("data/app/db")
					assertNoErrorResponse(t, resp)
					assertOperationIDs(t, resp.Data["metadata"].(map[string]interface{}), 0)
				} else {
					resp := env.delete("associations/app/db/" + associationIDFromResponse(t, association))
					if resp != nil && resp.IsError() {
						t.Fatalf("delete association: %v", resp.Error())
					}
				}
				assertQueueCount(t, env.b, env.storage, "pending", 0)
				env.runPeriodicAllowed("after local delete")
				if provider.remote != before {
					t.Fatalf("remote after %s delete = %#v, want unchanged %#v", target, provider.remote, before)
				}
			})
		}
	}
}

type retentionTestRemote struct {
	owner         providers.RequestIdentity
	payloadSHA256 string
	upserts       int
	deletes       int
}

type retentionTestProvider struct {
	fake.Provider
	remote retentionTestRemote
}

func (p *retentionTestProvider) OpenDestination(
	ctx context.Context,
	cfg providers.DestinationConfig,
) (providers.DestinationRuntime, error) {
	runtime, err := p.Provider.OpenDestination(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return retentionTestRuntime{DestinationRuntime: runtime, remote: &p.remote}, nil
}

type retentionTestRuntime struct {
	providers.DestinationRuntime
	remote *retentionTestRemote
}

func (r retentionTestRuntime) Upsert(ctx context.Context, req providers.UpsertRequest) (*providers.SyncResult, error) {
	result, err := r.DestinationRuntime.Upsert(ctx, req)
	if err == nil {
		r.remote.owner = req.OwnershipIdentity()
		r.remote.payloadSHA256 = req.PayloadSHA256
		r.remote.upserts++
	}
	return result, err
}

func (r retentionTestRuntime) Delete(ctx context.Context, req providers.DeleteRequest) (*providers.SyncResult, error) {
	result, err := r.DestinationRuntime.Delete(ctx, req)
	if err == nil {
		r.remote.owner = providers.RequestIdentity{}
		r.remote.payloadSHA256 = ""
		r.remote.deletes++
	}
	return result, err
}
