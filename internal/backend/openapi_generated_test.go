package backend

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/openbao/openbao/sdk/v2/framework"
	schematest "github.com/openbao/openbao/sdk/v2/helper/testhelpers/schema"
	"github.com/openbao/openbao/sdk/v2/logical"
)

type apiGoldenOperation struct {
	path      string
	operation logical.Operation
}

var apiGoldenOperations = map[string]apiGoldenOperation{
	"associations.list":         {path: "associations", operation: logical.ListOperation},
	"associations.plan.create":  {path: "associations/app/db/plan", operation: logical.UpdateOperation},
	"associations.read.path":    {path: "associations/app/db", operation: logical.ReadOperation},
	"associations.write.create": {path: "associations/app/db", operation: logical.UpdateOperation},
	"config.read.initial":       {path: configPath, operation: logical.ReadOperation},
	"config.update":             {path: configPath, operation: logical.UpdateOperation},
	"data.read.latest":          {path: "data/app/db", operation: logical.ReadOperation},
	"data.write.initial":        {path: "data/app/db", operation: logical.UpdateOperation},
	"destinations.check":        {path: "destinations/fake/default/check", operation: logical.ReadOperation},
	"destinations.health":       {path: "destinations/fake/default/health", operation: logical.ReadOperation},
	"destinations.list":         {path: "destinations/fake", operation: logical.ListOperation},
	"destinations.read":         {path: "destinations/fake/default", operation: logical.ReadOperation},
	"destinations.validate":     {path: "destinations/fake/default/validate", operation: logical.ReadOperation},
	"destinations.write.empty":  {path: "destinations/fake/default", operation: logical.UpdateOperation},
	"info.read":                 {path: "info", operation: logical.ReadOperation},
	"metadata.read":             {path: "metadata/app/db", operation: logical.ReadOperation},
	"metadata.write":            {path: "metadata/app/db", operation: logical.UpdateOperation},
	"queue.drain":               {path: "queue/drain", operation: logical.UpdateOperation},
	"queue.operation.read":      {path: "queue/op-placeholder", operation: logical.ReadOperation},
	"queue.read.pending":        {path: "queue", operation: logical.ReadOperation},
	"queue.read.synced":         {path: "queue", operation: logical.ReadOperation},
	"reconcile.apply.synced":    {path: "reconcile/app/db", operation: logical.UpdateOperation},
	"reconcile.plan.synced":     {path: "reconcile/app/db/plan", operation: logical.ReadOperation},
	"sources.check.empty":       {path: "sources/app/db/check", operation: logical.ReadOperation},
	"sources.check.ready":       {path: "sources/app/db/check", operation: logical.ReadOperation},
	"sources.disable":           {path: "sources/app/db/disable", operation: logical.UpdateOperation},
	"sources.enable":            {path: "sources/app/db/enable", operation: logical.UpdateOperation},
	"status.read.pending":       {path: "status/app/db", operation: logical.ReadOperation},
	"status.read.synced":        {path: "status/app/db", operation: logical.ReadOperation},
}

func TestNativeResponseSchemasCoverAPIGoldenResponses(t *testing.T) {
	backend := Backend(nil)
	golden := loadAPIGoldenResponses(t)

	for name, response := range golden {
		operation, ok := apiGoldenOperations[name]
		if !ok {
			t.Errorf("%s: API golden response has no backend operation mapping", name)
			continue
		}

		t.Run(name, func(t *testing.T) {
			path := backend.Route(operation.path)
			if path == nil {
				t.Fatalf("route %q is not registered", operation.path)
			}
			responseSchema := schematest.GetResponseSchema(t, path, operation.operation)
			schematest.ValidateResponseData(t, responseSchema, apiGoldenResponseData(t, response), true)
		})
	}

	for name := range apiGoldenOperations {
		if _, ok := golden[name]; !ok {
			t.Errorf("%s: backend operation mapping references a missing API golden response", name)
		}
	}
}

func TestBackendOperationsDeclareResponses(t *testing.T) {
	backend := Backend(nil)
	for _, path := range backend.Paths {
		for operation, handler := range path.Operations {
			if len(handler.Properties().Responses) == 0 {
				t.Errorf("%s %s: operation has no native response declaration", operation, path.Pattern)
			}
		}
	}
}

func TestBackendGeneratesOpenAPI(t *testing.T) {
	backend := Backend(nil)
	response, err := backend.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.HelpOperation,
		Path:      "",
		Data: map[string]interface{}{
			"requestResponsePrefix": apiOperationPrefix,
		},
	})
	if err != nil {
		t.Fatalf("generate OpenAPI: %v", err)
	}
	document, ok := response.Data["openapi"].(*framework.OASDocument)
	if !ok {
		t.Fatalf("generated OpenAPI has unexpected type %T", response.Data["openapi"])
	}
	if document.Version != framework.OASVersion {
		t.Errorf("OpenAPI version = %q, want %q", document.Version, framework.OASVersion)
	}
	if len(document.Components.Schemas) == 0 {
		t.Error("generated OpenAPI has no component schemas")
	}

	operationIDs := make(map[string]string)
	operationCount := 0
	for path, item := range document.Paths {
		for method, operation := range map[string]*framework.OASOperation{
			"GET":    item.Get,
			"POST":   item.Post,
			"DELETE": item.Delete,
		} {
			if operation == nil {
				continue
			}
			operationCount++
			if operation.Summary == "" {
				t.Errorf("%s %s: generated operation has no summary", method, path)
			}
			if !strings.HasPrefix(operation.OperationID, apiOperationPrefix+"-") {
				t.Errorf("%s %s: generated operation ID %q has no stable prefix", method, path, operation.OperationID)
			}
			if previous, exists := operationIDs[operation.OperationID]; exists {
				t.Errorf("%s %s: duplicate operation ID %q already used by %s", method, path, operation.OperationID, previous)
			}
			operationIDs[operation.OperationID] = method + " " + path
			if len(operation.Responses) == 0 {
				t.Errorf("%s %s: generated operation has no responses", method, path)
			}
		}
	}

	expectedOperations := generatedOperationCount(backend.Paths)
	if operationCount != expectedOperations {
		t.Errorf("generated operation count = %d, want %d from backend paths", operationCount, expectedOperations)
	}
}

func loadAPIGoldenResponses(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(apiGoldenFile)
	if err != nil {
		t.Fatalf("read API golden responses: %v", err)
	}
	var golden map[string]interface{}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode API golden responses: %v", err)
	}
	return golden
}

func apiGoldenResponseData(t *testing.T, response interface{}) map[string]interface{} {
	t.Helper()
	if response == nil {
		return nil
	}
	envelope, ok := response.(map[string]interface{})
	if !ok {
		t.Fatalf("API golden response has unexpected type %T", response)
	}
	data, ok := envelope["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("API golden response data has unexpected type %T", envelope["data"])
	}
	return data
}

func generatedOperationCount(paths []*framework.Path) int {
	count := 0
	for _, path := range paths {
		if path.Operations[logical.UpdateOperation] != nil || path.Operations[logical.CreateOperation] != nil {
			count++
		}
		if path.Operations[logical.ReadOperation] != nil || path.Operations[logical.ListOperation] != nil {
			count++
		}
		if path.Operations[logical.DeleteOperation] != nil {
			count++
		}
	}
	return count
}
