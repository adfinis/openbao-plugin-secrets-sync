// Package payload builds deterministic destination payload bytes.
package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const FormatJSON = "json"

// CanonicalPayload is the exact byte sequence the core sends to a provider.
type CanonicalPayload struct {
	Format string
	Bytes  []byte
	SHA256 string
}

// BuildJSON returns stable JSON bytes and a digest over those exact bytes.
func BuildJSON(data map[string]interface{}) (CanonicalPayload, error) {
	if len(data) == 0 {
		return CanonicalPayload{}, fmt.Errorf("payload data must contain at least one key")
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		return CanonicalPayload{}, err
	}
	payloadBytes := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	sum := sha256.Sum256(payloadBytes)
	return CanonicalPayload{
		Format: FormatJSON,
		Bytes:  payloadBytes,
		SHA256: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}
