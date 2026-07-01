package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func FuzzBuildRaw(f *testing.F) {
	f.Add("secret-canary")
	f.Add("")
	f.Add("line\nbreak")

	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip("input outside fuzz smoke size")
		}
		payload, err := BuildRaw(value)
		if err != nil {
			t.Fatalf("build raw: %v", err)
		}
		if payload.Format != FormatRaw {
			t.Fatalf("format = %s, want %s", payload.Format, FormatRaw)
		}
		if !bytes.Equal(payload.Bytes, []byte(value)) {
			t.Fatalf("payload bytes = %q, want %q", string(payload.Bytes), value)
		}
		sum := sha256.Sum256([]byte(value))
		wantSHA := "sha256:" + hex.EncodeToString(sum[:])
		if payload.SHA256 != wantSHA {
			t.Fatalf("sha = %s, want %s", payload.SHA256, wantSHA)
		}
	})
}

func FuzzBuildJSON(f *testing.F) {
	f.Add("password", "secret-canary")
	f.Add("unicode", "Gruezi")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, key string, value string) {
		if len(key) > 1024 || len(value) > 4096 {
			t.Skip("input outside fuzz smoke size")
		}
		first, err := BuildJSON(map[string]interface{}{key: value})
		if err != nil {
			t.Fatalf("build first json: %v", err)
		}
		second, err := BuildJSON(map[string]interface{}{key: value})
		if err != nil {
			t.Fatalf("build second json: %v", err)
		}
		if first.Format != FormatJSON {
			t.Fatalf("format = %s, want %s", first.Format, FormatJSON)
		}
		if !bytes.Equal(first.Bytes, second.Bytes) || first.SHA256 != second.SHA256 {
			t.Fatalf(
				"json payload not deterministic: %q/%s vs %q/%s",
				first.Bytes,
				first.SHA256,
				second.Bytes,
				second.SHA256,
			)
		}
		if !json.Valid(first.Bytes) {
			t.Fatalf("payload is not valid json: %q", first.Bytes)
		}
		sum := sha256.Sum256(first.Bytes)
		wantSHA := "sha256:" + hex.EncodeToString(sum[:])
		if first.SHA256 != wantSHA {
			t.Fatalf("sha = %s, want %s", first.SHA256, wantSHA)
		}
	})
}
