package endpointguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestIsRestrictedAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"loopback v4", "127.0.0.1", true},
		{"loopback v6", "::1", true},
		{"private RFC1918 10/8", "10.0.0.1", true},
		{"private RFC1918 172.16/12", "172.16.0.1", true},
		{"private RFC1918 192.168/16", "192.168.1.1", true},
		{"private RFC4193 ULA", "fc00::1", true},
		{"link-local v4", "169.254.1.1", true},
		{"link-local v6", "fe80::1", true},
		{"multicast v4", "224.0.0.1", true},
		{"multicast v6", "ff02::1", true},
		{"unspecified v4", "0.0.0.0", true},
		{"unspecified v6", "::", true},
		{"CGNAT / shared address space (RFC 6598)", "100.64.0.1", true},
		{"CGNAT upper bound", "100.127.255.255", true},
		{"this-network (RFC 791)", "0.0.0.5", true},
		{"IETF protocol assignments (RFC 6890)", "192.0.0.8", true},
		{"benchmarking (RFC 2544)", "198.18.0.5", true},
		{"reserved / class E (RFC 1112)", "240.0.0.1", true},
		{"broadcast", "255.255.255.255", true},
		{"public v4", "203.0.113.10", false},
		{"public v6", "2606:4700:4700::1111", false},
		{"just below CGNAT range", "100.63.255.255", false},
		{"just above CGNAT range", "100.128.0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRestrictedAddr(netip.MustParseAddr(tt.addr)); got != tt.want {
				t.Fatalf("IsRestrictedAddr(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestGuardedDialContextDialsResolvedAddressDirectly(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	resolverCalls := 0
	resolver := func(context.Context, string, string) ([]netip.Addr, error) {
		resolverCalls++
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	dial := GuardedDialContext(resolver, func(addr netip.Addr) bool { return addr.IsLoopback() })
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	conn, err := dial(context.Background(), "tcp4", net.JoinHostPort("provider.example", port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
}

func TestGuardedDialContextRejectsWholeResolutionBeforeDial(t *testing.T) {
	resolver := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("203.0.113.10"),
			netip.MustParseAddr("127.0.0.1"),
		}, nil
	}
	dial := GuardedDialContext(resolver, func(addr netip.Addr) bool { return !IsRestrictedAddr(addr) })
	_, err := dial(context.Background(), "tcp", "provider.example:443")
	if err == nil || !strings.Contains(err.Error(), "endpoint address is not allowed") {
		t.Fatalf("error = %v, want disallowed address", err)
	}
}

func TestGuardedDialContextValidatesIPLiteralWithoutDNS(t *testing.T) {
	resolver := func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("resolver must not be called")
	}
	dial := GuardedDialContext(resolver, func(addr netip.Addr) bool { return !addr.IsLoopback() })
	_, err := dial(context.Background(), "tcp", "127.0.0.1:443")
	if err == nil || !strings.Contains(err.Error(), "endpoint address is not allowed") {
		t.Fatalf("error = %v, want disallowed address", err)
	}
}

func TestGuardedDialContextRejectsEmptyResolution(t *testing.T) {
	dial := GuardedDialContext(
		func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil },
		func(netip.Addr) bool { return true },
	)
	_, err := dial(context.Background(), "tcp", "provider.example:443")
	if err == nil || !strings.Contains(err.Error(), "returned no addresses") {
		t.Fatalf("error = %v, want empty resolution error", err)
	}
}

func TestGuardedDialContextUnmapsIPv4Addresses(t *testing.T) {
	dial := GuardedDialContext(
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}, nil
		},
		func(addr netip.Addr) bool { return !addr.IsLoopback() },
	)
	_, err := dial(context.Background(), "tcp4", "provider.example:443")
	if err == nil || !strings.Contains(err.Error(), "endpoint address is not allowed") {
		t.Fatalf("error = %v, want mapped loopback rejection", err)
	}
}
