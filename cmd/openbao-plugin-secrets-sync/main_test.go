package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbao/openbao/api/v2"
)

// The legacy plugin TLS bootstrap is the plugin's go-jose parsing boundary.
// Exercise the OpenBao API helper rather than depending on go-jose internals.
func TestPluginTLSWrappingTokenCompatibility(t *testing.T) {
	for _, name := range []string{
		api.PluginAutoMTLSEnv, "VAULT_PLUGIN_AUTOMTLS_ENABLED",
		api.PluginMetadataModeEnv, "VAULT_PLUGIN_METADATA_MODE",
		"BAO_NAMESPACE", "VAULT_NAMESPACE",
	} {
		t.Setenv(name, "")
	}

	key, response := pluginTLSCertificate(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPut || r.URL.Path != "/v1/sys/wrapping/unwrap" {
			t.Errorf("unexpected unwrap request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(response); err != nil {
			t.Errorf("write unwrap response: %v", err)
		}
	}))
	defer server.Close()

	token := pluginWrappingToken(t, key, server.URL, "ES512")
	t.Setenv(api.PluginUnwrapTokenEnv, token)

	provider := api.VaultPluginTLSProvider(nil)
	if provider == nil {
		t.Fatal("legacy TLS provider is disabled")
	}
	tlsConfig, err := provider()
	if err != nil {
		t.Fatalf("unwrap an ES512 plugin token: %v", err)
	}
	if tlsConfig.ServerName != "plugin.test" || len(tlsConfig.Certificates) != 1 || tlsConfig.RootCAs == nil {
		t.Fatal("TLS bootstrap did not retain the wrapped certificate")
	}
	if requests.Load() != 1 {
		t.Fatalf("unwrap requests = %d, want 1", requests.Load())
	}

	for _, invalid := range []string{
		"malformed-token",
		pluginWrappingToken(t, key, server.URL, "RS256"),
	} {
		t.Setenv(api.PluginUnwrapTokenEnv, invalid)
		if _, err := provider(); err == nil || !strings.Contains(err.Error(), "error parsing wrapping token") {
			t.Fatalf("invalid wrapping token must fail before an unwrap request: %v", err)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("invalid wrapping token reached the unwrap endpoint")
	}
}

func pluginTLSCertificate(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "plugin.test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificate, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(struct {
		Data map[string]string `json:"data"`
	}{Data: map[string]string{
		"ServerCert": base64.StdEncoding.EncodeToString(certificate),
		"ServerKey":  base64.StdEncoding.EncodeToString(privateKey),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return key, response
}

func pluginWrappingToken(t *testing.T, key *ecdsa.PrivateKey, address, algorithm string) string {
	t.Helper()
	claims, err := json.Marshal(struct {
		Address string `json:"addr"`
	}{Address: address})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: algorithm, Type: "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	message := encode(header) + "." + encode(claims)
	digest := sha512.Sum512([]byte(message))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 132)
	r.FillBytes(signature[:66])
	s.FillBytes(signature[66:])
	return message + "." + encode(signature)
}
