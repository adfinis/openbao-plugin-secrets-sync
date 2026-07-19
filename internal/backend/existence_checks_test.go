package backend

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/adfinis/openbao-plugin-secrets-sync/internal/providers/gitlab"
	"github.com/openbao/openbao/sdk/v2/logical"
)

func TestWritableResourceExistenceChecks(t *testing.T) {
	env := newBackendTestEnv(t)

	assertBackendResourceExists(t, env.b, env.storage, "data/app/db", nil, false)
	assertBackendResourceExists(t, env.b, env.storage, "metadata/app/db", nil, false)
	assertBackendResourceExists(t, env.b, env.storage, "destinations/fake/default", nil, false)
	assertBackendResourceExists(t, env.b, env.storage, "associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
	}, false)

	env.writeAppDBSecret("initial")
	env.createFakeDestination("default")
	env.enableAppDBSourceSync()
	associationResp := env.update("associations/app/db", map[string]interface{}{
		"destination":   destinationRef(providerTypeFake, "default"),
		"resolved_name": "prod/app/db",
		"enabled":       false,
	})
	assertNoErrorResponse(t, associationResp)

	assertBackendResourceExists(t, env.b, env.storage, "data/app/db", nil, true)
	assertBackendResourceExists(t, env.b, env.storage, "metadata/app/db", nil, true)
	assertBackendResourceExists(t, env.b, env.storage, "destinations/fake/default", nil, true)
	assertBackendResourceExists(t, env.b, env.storage, "associations/app/db", map[string]interface{}{
		"destination": destinationRef(providerTypeFake, "default"),
	}, true)
}

func TestDataExistenceUsesMetadataRecord(t *testing.T) {
	env := newBackendTestEnv(t)
	resp := env.update("metadata/policy/only", map[string]interface{}{
		"max_versions": 3,
	})
	assertNoErrorResponse(t, resp)

	assertBackendResourceExists(t, env.b, env.storage, "metadata/policy/only", nil, true)
	assertBackendResourceExists(t, env.b, env.storage, "data/policy/only", nil, true)
}

func TestAssociationExistenceUsesNormalizedDestinationSelector(t *testing.T) {
	env := newBackendTestEnv(t)
	env.writeAppDBSecret("initial")
	createGitLabDestination(t, env)

	for _, scope := range []string{"staging", "production"} {
		resp := env.update("associations/app/db", map[string]interface{}{
			"destination":                    destinationRef(gitlab.ProviderType, "prod"),
			"resolved_name":                  "APP_DB",
			"enabled":                        false,
			gitlab.ConfigKeyEnvironmentScope: scope,
		})
		assertNoErrorResponse(t, resp)
	}

	assertBackendResourceExists(t, env.b, env.storage, "associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyEnvironmentScope: "staging",
	}, true)
	assertBackendResourceExists(t, env.b, env.storage, "associations/app/db", map[string]interface{}{
		"destination":                    destinationRef(gitlab.ProviderType, "prod"),
		gitlab.ConfigKeyEnvironmentScope: "development",
	}, false)

	// An underspecified selector that matches multiple existing associations
	// must still require update capability; the write callback reports the
	// ambiguity after ACL evaluation.
	assertBackendResourceExists(t, env.b, env.storage, "associations/app/db", map[string]interface{}{
		"destination": destinationRef(gitlab.ProviderType, "prod"),
	}, true)
}

func TestExistenceChecksTreatReadOnlyStorageAsMissing(t *testing.T) {
	b := newBackendForTest(&logical.BackendConfig{})
	checks := []struct {
		path string
		data map[string]interface{}
	}{
		{path: "data/app/db"},
		{path: "metadata/app/db"},
		{path: "destinations/fake/default"},
		{
			path: "associations/app/db",
			data: map[string]interface{}{"destination": destinationRef(providerTypeFake, "default")},
		},
		{
			path: "associations/app/gitlab",
			data: map[string]interface{}{
				"destination":                    destinationRef(gitlab.ProviderType, "prod"),
				gitlab.ConfigKeyEnvironmentScope: "production",
			},
		},
	}
	readOnlyErrors := []error{
		logical.ErrReadOnly,
		fmt.Errorf("setup storage: %w", logical.ErrSetupReadOnly),
	}

	for _, storageErr := range readOnlyErrors {
		for _, check := range checks {
			t.Run(storageErr.Error()+"/"+check.path, func(t *testing.T) {
				storage := &existenceCheckErrorStorage{
					Storage: &logical.InmemStorage{},
					err:     storageErr,
				}
				assertBackendResourceExists(t, b, storage, check.path, check.data, false)
			})
		}
	}
}

func TestExistenceChecksPropagateStorageErrors(t *testing.T) {
	b := newBackendForTest(&logical.BackendConfig{})
	storageErr := errors.New("storage unavailable")
	checks := []struct {
		path string
		data map[string]interface{}
	}{
		{path: "data/app/db"},
		{path: "metadata/app/db"},
		{path: "destinations/fake/default"},
		{
			path: "associations/app/db",
			data: map[string]interface{}{"destination": destinationRef(providerTypeFake, "default")},
		},
		{
			path: "associations/app/gitlab",
			data: map[string]interface{}{
				"destination":                    destinationRef(gitlab.ProviderType, "prod"),
				gitlab.ConfigKeyEnvironmentScope: "production",
			},
		},
	}
	for _, check := range checks {
		t.Run(check.path, func(t *testing.T) {
			storage := &existenceCheckErrorStorage{
				Storage: &logical.InmemStorage{},
				err:     storageErr,
			}
			checkFound, exists, err := b.HandleExistenceCheck(context.Background(), &logical.Request{
				Operation: logical.UpdateOperation,
				Path:      check.path,
				Storage:   storage,
				Data:      check.data,
			})
			if !checkFound {
				t.Fatal("HandleExistenceCheck did not find the check")
			}
			if exists {
				t.Fatal("HandleExistenceCheck reported existence after a storage error")
			}
			if err == nil || err.Error() != storageErr.Error() {
				t.Fatalf("HandleExistenceCheck error = %v, want %v", err, storageErr)
			}
		})
	}
}

func assertBackendResourceExists(
	t *testing.T,
	b *secretSyncBackend,
	storage logical.Storage,
	path string,
	data map[string]interface{},
	want bool,
) {
	t.Helper()
	checkFound, exists, err := b.HandleExistenceCheck(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      path,
		Storage:   storage,
		Data:      data,
	})
	if err != nil {
		t.Fatalf("HandleExistenceCheck(%q) error = %v", path, err)
	}
	if !checkFound {
		t.Fatalf("HandleExistenceCheck(%q) did not find a check", path)
	}
	if exists != want {
		t.Fatalf("HandleExistenceCheck(%q) exists = %t, want %t", path, exists, want)
	}
}

type existenceCheckErrorStorage struct {
	logical.Storage
	err error
}

func (s *existenceCheckErrorStorage) List(context.Context, string) ([]string, error) {
	return nil, s.err
}

func (s *existenceCheckErrorStorage) ListPage(context.Context, string, string, int) ([]string, error) {
	return nil, s.err
}

func (s *existenceCheckErrorStorage) Get(context.Context, string) (*logical.StorageEntry, error) {
	return nil, s.err
}
