package backend

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const openAPISpecFile = "../../docs/reference/api/openapi.yaml"

var apiGoldenOpenAPIResponseSchemas = map[string]string{
	"associations.list":         "ListResponse",
	"associations.plan.create":  "AssociationPlanResponse",
	"associations.read.path":    "AssociationsResponse",
	"associations.write.create": "AssociationLifecycleResponse",
	"config.read.initial":       "ConfigResponse",
	"data.read.latest":          "SourceDataResponse",
	"data.write.initial":        "SourceDataWriteResponse",
	"destinations.check":        "DestinationCheckResponse",
	"destinations.health":       "DestinationHealthResponse",
	"destinations.list":         "ListResponse",
	"destinations.read":         "DestinationResponse",
	"destinations.validate":     "DestinationValidationResponse",
	"info.read":                 "InfoResponse",
	"metadata.read":             "MetadataResponse",
	"metadata.write":            "MetadataResponse",
	"queue.drain":               "QueueDrainResponse",
	"queue.operation.read":      "QueueOperationResponse",
	"queue.read.pending":        "QueueSummaryResponse",
	"queue.read.synced":         "QueueSummaryResponse",
	"reconcile.apply.synced":    "ReconcileResponse",
	"reconcile.plan.synced":     "ReconcileResponse",
	"sources.check.empty":       "SourceCheckResponse",
	"sources.check.ready":       "SourceCheckResponse",
	"status.read.pending":       "StatusResponse",
	"status.read.synced":        "StatusResponse",
}

var apiGoldenEmptyResponses = map[string]bool{
	"config.update":            true,
	"destinations.write.empty": true,
}

func TestOpenAPIResponseSchemasCoverAPIGoldenResponses(t *testing.T) {
	golden := loadAPIGoldenResponses(t)
	schemas := loadOpenAPIComponentSchemas(t)

	goldenNames := sortedOpenAPIKeys(golden)
	for _, name := range goldenNames {
		response := golden[name]
		if response == nil {
			if !apiGoldenEmptyResponses[name] {
				t.Errorf("%s: empty API golden response must be recorded in apiGoldenEmptyResponses or mapped to a schema", name)
			}
			continue
		}

		schemaName, ok := apiGoldenOpenAPIResponseSchemas[name]
		if !ok {
			t.Errorf("%s: API golden response has no OpenAPI response schema mapping", name)
			continue
		}
		schema, ok := openAPIComponentSchema(schemas, schemaName)
		if !ok {
			t.Errorf("%s: OpenAPI schema %q is not defined", name, schemaName)
			continue
		}
		if errors := openAPISchemaCoverageErrors(schemas, schema, response, name); len(errors) > 0 {
			t.Errorf(
				"%s: OpenAPI schema %s does not cover golden response shape:\n%s",
				name,
				schemaName,
				strings.Join(errors, "\n"),
			)
		}
	}

	for name := range apiGoldenOpenAPIResponseSchemas {
		if _, ok := golden[name]; !ok {
			t.Errorf("%s: OpenAPI/golden mapping references a missing API golden response", name)
		}
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

func loadOpenAPIComponentSchemas(t *testing.T) map[string]interface{} {
	t.Helper()

	raw, err := os.ReadFile(openAPISpecFile)
	if err != nil {
		t.Fatalf("read OpenAPI spec: %v", err)
	}
	var spec map[string]interface{}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode OpenAPI spec: %v", err)
	}
	components, ok := openAPIObject(spec["components"])
	if !ok {
		t.Fatal("OpenAPI spec is missing components")
	}
	schemas, ok := openAPIObject(components["schemas"])
	if !ok {
		t.Fatal("OpenAPI spec is missing components.schemas")
	}
	return schemas
}

func openAPIComponentSchema(schemas map[string]interface{}, name string) (map[string]interface{}, bool) {
	return openAPIObject(schemas[name])
}

func openAPISchemaCoverageErrors(
	schemas map[string]interface{},
	schema map[string]interface{},
	value interface{},
	path string,
) []string {
	if ref, ok := schema["$ref"].(string); ok && ref != "" {
		schemaName, ok := strings.CutPrefix(ref, "#/components/schemas/")
		if !ok {
			return []string{fmt.Sprintf("%s: unsupported OpenAPI ref %q", path, ref)}
		}
		resolved, ok := openAPIComponentSchema(schemas, schemaName)
		if !ok {
			return []string{fmt.Sprintf("%s: unresolved OpenAPI ref %q", path, ref)}
		}
		return openAPISchemaCoverageErrors(schemas, resolved, value, path)
	}

	if oneOf, ok := openAPIList(schema["oneOf"]); ok {
		var firstErrors []string
		for _, candidate := range oneOf {
			candidateSchema, ok := openAPIObject(candidate)
			if !ok {
				continue
			}
			errors := openAPISchemaCoverageErrors(schemas, candidateSchema, value, path)
			if len(errors) == 0 {
				return nil
			}
			if firstErrors == nil {
				firstErrors = errors
			}
		}
		return []string{
			fmt.Sprintf("%s: no oneOf schema covers golden value; first mismatch: %s", path, strings.Join(firstErrors, "; ")),
		}
	}

	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		return openAPIObjectCoverageErrors(schemas, schema, typed, path)
	case []interface{}:
		return openAPIArrayCoverageErrors(schemas, schema, typed, path)
	default:
		return openAPIScalarCoverageErrors(schema, typed, path)
	}
}

func openAPIObjectCoverageErrors(
	schemas map[string]interface{},
	schema map[string]interface{},
	value map[string]interface{},
	path string,
) []string {
	properties, _ := openAPIObject(schema["properties"])
	additionalProperties, hasAdditionalProperties := schema["additionalProperties"]

	var errors []string
	for _, key := range sortedOpenAPIKeys(value) {
		propertyPath := path + "." + key
		propertyValue := value[key]
		if propertySchema, ok := properties[key]; ok {
			nestedSchema, ok := openAPIObject(propertySchema)
			if !ok {
				errors = append(errors, fmt.Sprintf("%s: OpenAPI property schema is not an object", propertyPath))
				continue
			}
			errors = append(errors, openAPISchemaCoverageErrors(schemas, nestedSchema, propertyValue, propertyPath)...)
			continue
		}

		if hasAdditionalProperties {
			errors = append(
				errors,
				openAPIAdditionalPropertiesCoverageErrors(schemas, additionalProperties, propertyValue, propertyPath)...,
			)
			continue
		}

		errors = append(
			errors,
			fmt.Sprintf("%s: field is present in API golden response but missing from OpenAPI schema", propertyPath),
		)
	}
	return errors
}

func openAPIAdditionalPropertiesCoverageErrors(
	schemas map[string]interface{},
	additionalProperties interface{},
	value interface{},
	path string,
) []string {
	switch typed := additionalProperties.(type) {
	case bool:
		if typed {
			return nil
		}
		return []string{fmt.Sprintf("%s: field is present in API golden response but additionalProperties is false", path)}
	default:
		schema, ok := openAPIObject(typed)
		if !ok {
			return []string{fmt.Sprintf("%s: OpenAPI additionalProperties schema is not an object", path)}
		}
		return openAPISchemaCoverageErrors(schemas, schema, value, path)
	}
}

func openAPIArrayCoverageErrors(
	schemas map[string]interface{},
	schema map[string]interface{},
	value []interface{},
	path string,
) []string {
	if len(value) == 0 {
		return nil
	}
	itemSchema, ok := openAPIObject(schema["items"])
	if !ok {
		return []string{fmt.Sprintf("%s: array field is missing OpenAPI items schema", path)}
	}

	var errors []string
	for index, item := range value {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		errors = append(errors, openAPISchemaCoverageErrors(schemas, itemSchema, item, itemPath)...)
	}
	return errors
}

func openAPIScalarCoverageErrors(schema map[string]interface{}, value interface{}, path string) []string {
	types := openAPISchemaTypes(schema)
	if len(types) == 0 {
		return nil
	}

	switch typed := value.(type) {
	case string:
		if openAPITypeAllowed(types, "string") {
			return nil
		}
	case bool:
		if openAPITypeAllowed(types, "boolean") {
			return nil
		}
	case float64:
		if openAPITypeAllowed(types, "number") {
			return nil
		}
		if openAPITypeAllowed(types, "integer") && math.Trunc(typed) == typed {
			return nil
		}
	}
	return []string{
		fmt.Sprintf("%s: golden value type %T is not covered by OpenAPI type %s", path, value, strings.Join(types, "|")),
	}
}

func openAPISchemaTypes(schema map[string]interface{}) []string {
	switch typed := schema["type"].(type) {
	case string:
		return []string{typed}
	case []interface{}:
		types := make([]string, 0, len(typed))
		for _, value := range typed {
			if schemaType, ok := value.(string); ok {
				types = append(types, schemaType)
			}
		}
		return types
	default:
		return nil
	}
}

func openAPITypeAllowed(types []string, allowed string) bool {
	for _, schemaType := range types {
		if schemaType == allowed {
			return true
		}
	}
	return false
}

func openAPIObject(value interface{}) (map[string]interface{}, bool) {
	typed, ok := value.(map[string]interface{})
	return typed, ok
}

func openAPIList(value interface{}) ([]interface{}, bool) {
	typed, ok := value.([]interface{})
	return typed, ok
}

func sortedOpenAPIKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
