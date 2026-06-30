package observability

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestMetricNamesUseOpenTelemetryInstrumentShape(t *testing.T) {
	metricNames := []string{
		MetricQueueDepth,
		MetricOperations,
		MetricProviderRequests,
		MetricProviderRequestDuration,
		MetricReconcileRuns,
		MetricRestoreGuardActive,
	}

	for _, metricName := range metricNames {
		if !strings.HasPrefix(metricName, "openbao.secret_sync.") {
			t.Fatalf("metric %q does not use openbao.secret_sync prefix", metricName)
		}
		if strings.Contains(metricName, "_total") || strings.Contains(metricName, "_seconds") {
			t.Fatalf("metric %q includes exporter-specific suffix", metricName)
		}
	}
}

func TestMetricAttributesStayLowCardinality(t *testing.T) {
	testCases := []struct {
		name       string
		attributes []attribute.KeyValue
	}{
		{
			name: "operation",
			attributes: operationAttributes(OperationEvent{
				Operation:       OperationUpsert,
				Result:          ResultSuccess,
				DestinationType: "aws-sm",
				Granularity:     "secret-path",
			}),
		},
		{
			name: "provider request",
			attributes: providerRequestAttributes(ProviderRequestEvent{
				Provider:  "aws-sm",
				Operation: OperationReadState,
				Result:    ResultFailure,
			}),
		},
		{
			name: "reconcile run",
			attributes: reconcileRunAttributes(ReconcileRunEvent{
				Result:          ResultFailure,
				ErrorClass:      "ownership",
				DestinationType: "aws-sm",
				Granularity:     "secret-path",
			}),
		},
	}

	allowedKeys := map[attribute.Key]struct{}{
		AttributeProvider:        {},
		AttributeDestinationType: {},
		AttributeOperation:       {},
		AttributeState:           {},
		AttributeResult:          {},
		AttributeErrorClass:      {},
		AttributeGranularity:     {},
	}
	forbiddenKeys := map[attribute.Key]struct{}{
		"path":             {},
		"source_path":      {},
		"resolved_name":    {},
		"remote_name":      {},
		"destination_name": {},
		"association_id":   {},
		"operation_id":     {},
		"payload_sha256":   {},
		"remote_version":   {},
		"aws_arn":          {},
		"account_id":       {},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, attr := range testCase.attributes {
				if _, forbidden := forbiddenKeys[attr.Key]; forbidden {
					t.Fatalf("forbidden metric attribute key %q", attr.Key)
				}
				if _, allowed := allowedKeys[attr.Key]; !allowed {
					t.Fatalf("unexpected metric attribute key %q", attr.Key)
				}
			}
		})
	}
}

func TestBlankAttributeValuesAreNormalized(t *testing.T) {
	operationAttrs := operationAttributes(OperationEvent{})
	assertAttributeValue(t, operationAttrs, AttributeOperation, ValueUnknown)
	assertAttributeValue(t, operationAttrs, AttributeResult, ValueUnknown)
	assertAttributeValue(t, operationAttrs, AttributeErrorClass, ValueNone)

	providerAttrs := providerRequestAttributes(ProviderRequestEvent{})
	assertAttributeValue(t, providerAttrs, AttributeProvider, ValueUnknown)
	assertAttributeValue(t, providerAttrs, AttributeErrorClass, ValueNone)
}

func assertAttributeValue(
	t *testing.T,
	attributes []attribute.KeyValue,
	key attribute.Key,
	expected string,
) {
	t.Helper()
	for _, attr := range attributes {
		if attr.Key == key {
			if got := attr.Value.AsString(); got != expected {
				t.Fatalf("attribute %s = %q, want %q", key, got, expected)
			}
			return
		}
	}
	t.Fatalf("attribute %s not found in %#v", key, attributes)
}
