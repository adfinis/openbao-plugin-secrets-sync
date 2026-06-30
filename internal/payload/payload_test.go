package payload

import "testing"

func TestBuildJSONIsDeterministic(t *testing.T) {
	first, err := BuildJSON(map[string]interface{}{
		"username": "app",
		"password": "secret",
	})
	if err != nil {
		t.Fatalf("build first payload: %v", err)
	}
	second, err := BuildJSON(map[string]interface{}{
		"password": "secret",
		"username": "app",
	})
	if err != nil {
		t.Fatalf("build second payload: %v", err)
	}
	if string(first.Bytes) != string(second.Bytes) {
		t.Fatalf("payload bytes differ:\nfirst:  %s\nsecond: %s", first.Bytes, second.Bytes)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("payload hashes differ: %s != %s", first.SHA256, second.SHA256)
	}
	if got := string(first.Bytes); got != `{"password":"secret","username":"app"}` {
		t.Fatalf("payload bytes = %s", got)
	}
}

func TestBuildRawUsesExactValueBytes(t *testing.T) {
	payload, err := BuildRaw("line one\nline two")
	if err != nil {
		t.Fatalf("build raw: %v", err)
	}
	if payload.Format != FormatRaw {
		t.Fatalf("format = %s, want %s", payload.Format, FormatRaw)
	}
	if got := string(payload.Bytes); got != "line one\nline two" {
		t.Fatalf("payload bytes = %q", got)
	}
	if payload.SHA256 == "" {
		t.Fatal("payload hash must be set")
	}
}

func TestBuildRawRejectsStructuredValues(t *testing.T) {
	if _, err := BuildRaw(map[string]interface{}{"password": "secret"}); err == nil {
		t.Fatal("build raw structured value must fail")
	}
}
