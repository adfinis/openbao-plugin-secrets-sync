package backend

import (
	"strings"
	"testing"
)

func TestErrorResponseWithDiagnosticPreservesOpenBaoErrorShape(t *testing.T) {
	resp := errorResponseWithDiagnostic("blocked", queueCapacityDiagnostic("secret-sync"))
	if resp == nil || !resp.IsError() {
		t.Fatalf("response = %#v, want OpenBao error response", resp)
	}
	errorText := resp.Error().Error()
	for _, want := range []string{
		"blocked",
		"Hint: Queue capacity is exhausted",
		"Next action: bao read secret-sync/queue",
	} {
		if !strings.Contains(errorText, want) {
			t.Fatalf("error text = %q, want substring %q", errorText, want)
		}
	}
	data, ok := resp.Data["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("error data = %T, want map[string]interface{}", resp.Data["data"])
	}
	assertHintContains(t, data, "Queue capacity is exhausted")
	assertNextActionCommand(t, data, "read_queue", "bao read secret-sync/queue")
}
