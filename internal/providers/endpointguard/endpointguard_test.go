package endpointguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

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
