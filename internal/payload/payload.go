// Package payload builds deterministic destination payload bytes.
package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	FormatJSON    = "json"
	FormatRaw     = "raw"
	FormatDataMap = "data-map"
)

// CanonicalPayload is the exact byte sequence the core sends to a provider.
type CanonicalPayload struct {
	Format string
	Bytes  []byte
	SHA256 string
	Data   map[string][]byte
}

// BuildJSON returns stable JSON bytes and a digest over those exact bytes.
func BuildJSON(data map[string]interface{}) (CanonicalPayload, error) {
	if len(data) == 0 {
		return CanonicalPayload{}, fmt.Errorf("payload data must contain at least one key")
	}
	payloadBytes, payloadHash, err := canonicalJSON(data)
	if err != nil {
		return CanonicalPayload{}, err
	}
	return CanonicalPayload{
		Format: FormatJSON,
		Bytes:  payloadBytes,
		SHA256: payloadHash,
	}, nil
}

// BuildRaw returns the exact bytes of a selected source value and a digest over those bytes.
func BuildRaw(value interface{}) (CanonicalPayload, error) {
	payloadBytes, err := RawBytes(value)
	if err != nil {
		return CanonicalPayload{}, err
	}
	return CanonicalPayload{
		Format: FormatRaw,
		Bytes:  payloadBytes,
		SHA256: payloadSHA256(payloadBytes),
	}, nil
}

// BuildDataMap returns deterministic bytes for a destination-native key/value map.
func BuildDataMap(data map[string][]byte) (CanonicalPayload, error) {
	if len(data) == 0 {
		return CanonicalPayload{}, fmt.Errorf("data-map payload must contain at least one key")
	}
	copied := copyDataMap(data)
	payloadBytes, payloadHash, err := canonicalJSON(copied)
	if err != nil {
		return CanonicalPayload{}, err
	}
	return CanonicalPayload{
		Format: FormatDataMap,
		Bytes:  payloadBytes,
		SHA256: payloadHash,
		Data:   copied,
	}, nil
}

func canonicalJSON(value interface{}) ([]byte, string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, "", err
	}
	payloadBytes := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	return payloadBytes, payloadSHA256(payloadBytes), nil
}

func payloadSHA256(payloadBytes []byte) string {
	sum := sha256.Sum256(payloadBytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// RawBytes returns the exact bytes accepted by raw and data-map payload modes.
func RawBytes(value interface{}) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("raw payload value must be string or bytes")
	}
}

func copyDataMap(input map[string][]byte) map[string][]byte {
	output := make(map[string][]byte, len(input))
	for key, value := range input {
		output[key] = append([]byte(nil), value...)
	}
	return output
}
