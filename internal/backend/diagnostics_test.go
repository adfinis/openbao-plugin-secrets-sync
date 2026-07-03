package backend

import "testing"

func TestErrorResponseWithDiagnosticPreservesOpenBaoErrorShape(t *testing.T) {
	resp := errorResponseWithDiagnostic("blocked", queueCapacityDiagnostic("secret-sync"))
	if resp == nil || !resp.IsError() {
		t.Fatalf("response = %#v, want OpenBao error response", resp)
	}
	data, ok := resp.Data["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("error data = %T, want map[string]interface{}", resp.Data["data"])
	}
	assertHintContains(t, data, "Queue capacity is exhausted")
	assertNextActionCommand(t, data, "read_queue", "bao read secret-sync/queue")
}
