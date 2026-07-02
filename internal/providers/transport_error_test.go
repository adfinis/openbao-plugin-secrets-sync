package providers

import (
	"context"
	"errors"
	"net"
	"net/url"
	"syscall"
	"testing"
)

type timeoutError struct{}

func (timeoutError) Error() string {
	return "timed out"
}

func (timeoutError) Timeout() bool {
	return true
}

func TestClassifyTransportError(t *testing.T) {
	tests := map[string]struct {
		err error
		ok  bool
	}{
		"context deadline": {
			err: context.DeadlineExceeded,
			ok:  true,
		},
		"net timeout": {
			err: timeoutError{},
			ok:  true,
		},
		"dns failure": {
			err: &net.DNSError{Err: "no such host", Name: "provider.example"},
			ok:  true,
		},
		"url transport wrapper": {
			err: &url.Error{Op: "Get", URL: "https://provider.example", Err: errors.New("tls handshake failed")},
			ok:  true,
		},
		"connection refused": {
			err: syscall.ECONNREFUSED,
			ok:  true,
		},
		"plain error": {
			err: errors.New("provider rejected the request"),
		},
		"nil": {},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			class, ok := ClassifyTransportError(tt.err)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if class != ErrorClassUnavailable {
				t.Fatalf("class = %s, want %s", class, ErrorClassUnavailable)
			}
		})
	}
}
