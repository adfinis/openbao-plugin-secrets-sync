package backend

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/logical"
)

var openAPIMethodNames = map[string]bool{
	"delete": true,
	"get":    true,
	"post":   true,
}

var openAPIPathParameterPattern = regexp.MustCompile(`\{([^}]+)\}`)

func TestOpenAPIPathsCoverBackendPathSurface(t *testing.T) {
	backendSurface := backendOpenAPIPathSurface(t)
	openAPISurface := specOpenAPIPathSurface(t)

	assertStringSetEqual(t, openAPISurface.paths(), backendSurface.paths(), "OpenAPI paths")
	for _, path := range sortedStringSet(backendSurface.paths()) {
		assertStringSetEqual(
			t,
			openAPISurface[path],
			backendSurface[path],
			fmt.Sprintf("OpenAPI methods for %s", path),
		)
	}
}

func TestOpenAPIRequestSchemasCoverBackendRequestFields(t *testing.T) {
	spec := loadOpenAPISpec(t)
	schemas, ok := openAPIComponentSchemas(spec)
	if !ok {
		t.Fatal("OpenAPI spec is missing components.schemas")
	}

	for _, backendPath := range openAPIBackendPaths(t) {
		openAPIPath := frameworkPatternToOpenAPIPath(t, backendPath.Pattern)
		openAPIPathItem, ok := openAPIPathItem(spec, openAPIPath)
		if !ok {
			t.Fatalf("%s: OpenAPI path is missing", openAPIPath)
		}

		for operation := range backendPath.Operations {
			method, ok := logicalOperationToOpenAPIMethod(operation)
			if !ok || method != "post" {
				continue
			}

			bodyFields := backendRequestBodyFields(backendPath, openAPIPath)
			operationObject, ok := openAPIObject(openAPIPathItem[method])
			if !ok {
				t.Fatalf("%s %s: OpenAPI operation is missing", strings.ToUpper(method), openAPIPath)
			}
			requestSchema, hasRequestBody := openAPIRequestBodySchema(schemas, operationObject)
			if !hasRequestBody {
				assertNoBackendBodyFields(t, openAPIPath, bodyFields)
				continue
			}

			properties, ok := openAPIObject(requestSchema["properties"])
			if !ok {
				t.Fatalf("%s %s: OpenAPI request schema is missing properties", strings.ToUpper(method), openAPIPath)
			}
			assertRequestFieldSetMatches(t, openAPIPath, bodyFields, properties)
			assertRequestFieldTypesMatch(t, openAPIPath, bodyFields, properties)
		}
	}
}

func backendOpenAPIPathSurface(t *testing.T) openAPIPathSurface {
	t.Helper()

	surface := make(openAPIPathSurface)
	for _, path := range openAPIBackendPaths(t) {
		openAPIPath := frameworkPatternToOpenAPIPath(t, path.Pattern)
		if surface[openAPIPath] == nil {
			surface[openAPIPath] = make(map[string]bool)
		}
		for operation := range path.Operations {
			method, ok := logicalOperationToOpenAPIMethod(operation)
			if ok {
				surface[openAPIPath][method] = true
			}
		}
	}
	return surface
}

func specOpenAPIPathSurface(t *testing.T) openAPIPathSurface {
	t.Helper()

	spec := loadOpenAPISpec(t)
	paths, ok := openAPIObject(spec["paths"])
	if !ok {
		t.Fatal("OpenAPI spec is missing paths")
	}

	surface := make(openAPIPathSurface)
	for _, path := range sortedOpenAPIKeys(paths) {
		pathItem, ok := openAPIObject(paths[path])
		if !ok {
			t.Fatalf("%s: OpenAPI path item is not an object", path)
		}
		if surface[path] == nil {
			surface[path] = make(map[string]bool)
		}
		for method := range pathItem {
			if openAPIMethodNames[method] {
				surface[path][method] = true
			}
		}
	}
	return surface
}

func openAPIBackendPaths(t *testing.T) []*framework.Path {
	t.Helper()

	backend := Backend(nil)
	if backend.Backend == nil {
		t.Fatal("backend framework is not initialized")
	}
	return backend.Paths
}

func frameworkPatternToOpenAPIPath(t *testing.T, pattern string) string {
	t.Helper()

	replacements := map[string]string{
		`(?P<association_id>assoc-[0-9a-f]{32})`: `{association_id}`,
		`(?P<operation_id>\w(([\w-.]+)?\w)?)`:    `{operation_id}`,
		`(?P<type>\w(([\w-.]+)?\w)?)`:            `{type}`,
		`(?P<name>\w(([\w-.]+)?\w)?)`:            `{name}`,
		`(?P<path>.*)`:                           `{path}`,
		`(?P<path>.+)`:                           `{path}`,
	}

	normalized := pattern
	for from, to := range replacements {
		normalized = strings.ReplaceAll(normalized, from, to)
	}
	normalized = strings.TrimSuffix(normalized, "/?")
	if strings.Contains(normalized, "(?P<") {
		t.Fatalf("%s: unsupported framework path pattern", pattern)
	}
	return "/" + normalized
}

func logicalOperationToOpenAPIMethod(operation logical.Operation) (string, bool) {
	switch operation {
	case logical.CreateOperation, logical.UpdateOperation:
		return "post", true
	case logical.ReadOperation, logical.ListOperation:
		return "get", true
	case logical.DeleteOperation:
		return "delete", true
	default:
		return "", false
	}
}

func openAPIPathItem(spec map[string]interface{}, path string) (map[string]interface{}, bool) {
	paths, ok := openAPIObject(spec["paths"])
	if !ok {
		return nil, false
	}
	return openAPIObject(paths[path])
}

func openAPIComponentSchemas(spec map[string]interface{}) (map[string]interface{}, bool) {
	components, ok := openAPIObject(spec["components"])
	if !ok {
		return nil, false
	}
	return openAPIObject(components["schemas"])
}

func openAPIRequestBodySchema(
	schemas map[string]interface{},
	operationObject map[string]interface{},
) (map[string]interface{}, bool) {
	requestBody, ok := openAPIObject(operationObject["requestBody"])
	if !ok {
		return nil, false
	}
	content, ok := openAPIObject(requestBody["content"])
	if !ok {
		return nil, false
	}
	jsonContent, ok := openAPIObject(content["application/json"])
	if !ok {
		return nil, false
	}
	schema, ok := openAPIObject(jsonContent["schema"])
	if !ok {
		return nil, false
	}
	return resolveOpenAPISchema(schemas, schema), true
}

func resolveOpenAPISchema(schemas map[string]interface{}, schema map[string]interface{}) map[string]interface{} {
	ref, ok := schema["$ref"].(string)
	if !ok || ref == "" {
		return schema
	}
	schemaName, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok {
		return schema
	}
	resolved, ok := openAPIComponentSchema(schemas, schemaName)
	if !ok {
		return schema
	}
	return resolved
}

func backendRequestBodyFields(path *framework.Path, openAPIPath string) map[string]*framework.FieldSchema {
	pathParameters := openAPIPathParameters(openAPIPath)
	fields := make(map[string]*framework.FieldSchema)
	for name, field := range path.Fields {
		if pathParameters[name] || openAPIFrameworkQueryField(name) {
			continue
		}
		if openAPIFrameworkPathFieldIgnored(openAPIPath, name) {
			continue
		}
		fields[name] = field
	}
	return fields
}

func openAPIPathParameters(path string) map[string]bool {
	matches := openAPIPathParameterPattern.FindAllStringSubmatch(path, -1)
	parameters := make(map[string]bool, len(matches))
	for _, match := range matches {
		parameters[match[1]] = true
	}
	return parameters
}

func openAPIFrameworkQueryField(name string) bool {
	switch name {
	case listAfterField, listLimitField, "version":
		return true
	default:
		return false
	}
}

func openAPIFrameworkPathFieldIgnored(path string, field string) bool {
	switch {
	case strings.HasPrefix(path, "/associations/{path}/{association_id}/") && field == "destination":
		return true
	case strings.HasPrefix(path, "/associations/{path}/") && field == "association_id":
		return true
	default:
		return false
	}
}

func assertNoBackendBodyFields(t *testing.T, path string, bodyFields map[string]*framework.FieldSchema) {
	t.Helper()

	if len(bodyFields) == 0 {
		return
	}
	t.Fatalf("%s: backend declares body fields without an OpenAPI request body: %v", path, sortedFieldKeys(bodyFields))
}

func assertRequestFieldSetMatches(
	t *testing.T,
	path string,
	bodyFields map[string]*framework.FieldSchema,
	properties map[string]interface{},
) {
	t.Helper()

	bodyFieldNames := sortedFieldKeys(bodyFields)
	propertyNames := sortedOpenAPIKeys(properties)
	if strings.Join(bodyFieldNames, "\x00") != strings.Join(propertyNames, "\x00") {
		t.Fatalf(
			"%s: OpenAPI request fields %v do not match backend fields %v",
			path,
			propertyNames,
			bodyFieldNames,
		)
	}
}

func assertRequestFieldTypesMatch(
	t *testing.T,
	path string,
	bodyFields map[string]*framework.FieldSchema,
	properties map[string]interface{},
) {
	t.Helper()

	for _, fieldName := range sortedFieldKeys(bodyFields) {
		propertySchema, ok := openAPIObject(properties[fieldName])
		if !ok {
			t.Fatalf("%s.%s: OpenAPI property schema is not an object", path, fieldName)
		}
		openAPIType := openAPIRequestPropertyType(propertySchema)
		backendType, ok := openAPIFieldType(bodyFields[fieldName].Type)
		if !ok {
			t.Fatalf("%s.%s: unsupported backend field type %s", path, fieldName, bodyFields[fieldName].Type)
		}
		if openAPIType != backendType {
			t.Fatalf(
				"%s.%s: OpenAPI type %q does not match backend field type %q",
				path,
				fieldName,
				openAPIType,
				backendType,
			)
		}
	}
}

func openAPIRequestPropertyType(schema map[string]interface{}) string {
	if _, ok := schema["items"]; ok {
		return "array"
	}
	types := openAPISchemaTypes(schema)
	if len(types) == 0 {
		return ""
	}
	sort.Strings(types)
	return strings.Join(types, "|")
}

func openAPIFieldType(fieldType framework.FieldType) (string, bool) {
	switch fieldType {
	case framework.TypeString, framework.TypeLowerCaseString, framework.TypeNameString:
		return "string", true
	case framework.TypeInt, framework.TypeInt64, framework.TypeDurationSecond, framework.TypeSignedDurationSecond:
		return "integer", true
	case framework.TypeBool:
		return "boolean", true
	case framework.TypeFloat:
		return "number", true
	case framework.TypeMap, framework.TypeKVPairs:
		return "object", true
	case framework.TypeSlice, framework.TypeStringSlice, framework.TypeCommaStringSlice, framework.TypeCommaIntSlice:
		return "array", true
	default:
		return "", false
	}
}

func assertStringSetEqual(t *testing.T, actual map[string]bool, expected map[string]bool, label string) {
	t.Helper()

	actualValues := sortedStringSet(actual)
	expectedValues := sortedStringSet(expected)
	if strings.Join(actualValues, "\x00") != strings.Join(expectedValues, "\x00") {
		t.Fatalf("%s mismatch:\nactual:   %v\nexpected: %v", label, actualValues, expectedValues)
	}
}

func sortedStringSet(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedFieldKeys(values map[string]*framework.FieldSchema) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type openAPIPathSurface map[string]map[string]bool

func (surface openAPIPathSurface) paths() map[string]bool {
	paths := make(map[string]bool, len(surface))
	for path := range surface {
		paths[path] = true
	}
	return paths
}
