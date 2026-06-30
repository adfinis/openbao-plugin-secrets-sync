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
