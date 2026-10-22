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
		{"public v4", "8.8.8.8", false},
		{"public v6", "2606:4700:4700::1111", false},
		{"just below CGNAT range", "100.63.255.255", false},
		{"just above CGNAT range", "100.128.0.0", false},
		{"CGNAT lower bound", "100.64.0.0", true},
		{"this-network upper bound", "0.255.255.255", true},
		{"above this-network", "1.0.0.0", false},
		{"IETF lower bound", "192.0.0.0", true},
		{"PCP anycast exception", "192.0.0.9", false},
		{"TURN anycast exception", "192.0.0.10", false},
		{"above anycast exceptions", "192.0.0.11", true},
		{"IETF upper bound", "192.0.0.255", true},
		{"below IETF assignments", "191.255.255.255", false},
		{"above IETF assignments", "192.0.1.0", false},
		{"TEST-NET-1 lower bound", "192.0.2.0", true},
		{"TEST-NET-1 upper bound", "192.0.2.255", true},
		{"below TEST-NET-1", "192.0.1.255", false},
		{"above TEST-NET-1", "192.0.3.0", false},
		{"AS112 v4", "192.31.196.1", false},
		{"AMT v4", "192.52.193.1", false},
		{"deprecated 6to4 lower bound", "192.88.99.0", true},
		{"6a44 relay", "192.88.99.2", true},
		{"deprecated 6to4 upper bound", "192.88.99.255", true},
		{"below deprecated 6to4", "192.88.98.255", false},
		{"above deprecated 6to4", "192.88.100.0", false},
		{"direct delegation AS112 v4", "192.175.48.1", false},
		{"benchmarking lower bound", "198.18.0.0", true},
		{"benchmarking upper bound", "198.19.255.255", true},
		{"below benchmarking", "198.17.255.255", false},
		{"above benchmarking", "198.20.0.0", false},
		{"TEST-NET-2 lower bound", "198.51.100.0", true},
		{"TEST-NET-2 upper bound", "198.51.100.255", true},
		{"below TEST-NET-2", "198.51.99.255", false},
		{"above TEST-NET-2", "198.51.101.0", false},
		{"TEST-NET-3 lower bound", "203.0.113.0", true},
		{"TEST-NET-3 upper bound", "203.0.113.255", true},
		{"below TEST-NET-3", "203.0.112.255", false},
		{"above TEST-NET-3", "203.0.114.0", false},
		{"reserved lower bound", "240.0.0.0", true},
		{"IPv4-compatible v6", "::8.8.8.8", true},
		{"NAT64 public v4", "64:ff9b::808:808", false},
		{"NAT64 PCP exception", "64:ff9b::c000:9", false},
		{"NAT64 private v4", "64:ff9b::a00:1", true},
		{"NAT64 CGNAT v4", "64:ff9b::6440:1", true},
		{"NAT64 documentation v4", "64:ff9b::cb00:710a", true},
		{"local NAT64 lower bound", "64:ff9b:1::", true},
		{"local NAT64 upper bound", "64:ff9b:1:ffff:ffff:ffff:ffff:ffff", true},
		{"above local NAT64", "64:ff9b:2::", false},
		{"discard lower bound", "100::", true},
		{"discard upper bound", "100::ffff:ffff:ffff:ffff", true},
		{"dummy v6 lower bound", "100:0:0:1::", true},
		{"dummy v6 upper bound", "100::1:ffff:ffff:ffff:ffff", true},
		{"above dummy v6", "100:0:0:2::", false},
		{"Teredo", "2001::1", true},
		{"below v6 anycast exceptions", "2001:1::", true},
		{"PCP v6 exception", "2001:1::1", false},
		{"TURN v6 exception", "2001:1::2", false},
		{"DNS-SD v6 exception", "2001:1::3", false},
		{"above v6 anycast exceptions", "2001:1::4", true},
		{"benchmarking v6", "2001:2::1", true},
		{"below AMT v6", "2001:2:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"AMT v6 lower bound", "2001:3::", false},
		{"AMT v6 upper bound", "2001:3:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"AS112 v6 lower bound", "2001:4:112::", false},
		{"AS112 v6 upper bound", "2001:4:112:ffff:ffff:ffff:ffff:ffff", false},
		{"below AS112 v6", "2001:4:111:ffff:ffff:ffff:ffff:ffff", true},
		{"above AS112 v6", "2001:4:113::", true},
		{"deprecated ORCHID", "2001:10::1", true},
		{"below ORCHIDv2", "2001:1f:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"ORCHIDv2 lower bound", "2001:20::", false},
		{"ORCHIDv2 upper bound", "2001:2f:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"DETs lower bound", "2001:30::", false},
		{"DETs upper bound", "2001:3f:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"above DETs", "2001:40::", true},
		{"IETF v6 upper bound", "2001:1ff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"above IETF v6", "2001:200::", false},
		{"documentation v6 lower bound", "2001:db8::", true},
		{"documentation v6 upper bound", "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"below documentation v6", "2001:db7:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"above documentation v6", "2001:db9::", false},
		{"6to4 v6 lower bound", "2002::", true},
		{"6to4 v6 upper bound", "2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"above 6to4 v6", "2003::", false},
		{"direct delegation AS112 v6", "2620:4f:8000::1", false},
		{"new documentation v6 lower bound", "3fff::", true},
		{"new documentation v6 upper bound", "3fff:fff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"below new documentation v6", "3ffe:ffff:ffff:ffff:ffff:ffff:ffff:ffff", false},
		{"above new documentation v6", "3fff:1000::", false},
		{"segment routing lower bound", "5f00::", true},
		{"segment routing upper bound", "5f00:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"above segment routing", "5f01::", false},
		{"site-local lower bound", "fec0::", true},
		{"site-local upper bound", "feff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tt.addr)
			if got := IsRestrictedAddr(addr); got != tt.want {
				t.Fatalf("IsRestrictedAddr(%s) = %v, want %v", tt.addr, got, tt.want)
			}
			if got := IsRestrictedHost(tt.addr); got != tt.want {
				t.Fatalf("IsRestrictedHost(%s) = %v, want %v", tt.addr, got, tt.want)
			}
			if addr.Is4() {
				mapped := netip.MustParseAddr("::ffff:" + tt.addr)
				if got := IsRestrictedAddr(mapped); got != tt.want {
					t.Fatalf("IsRestrictedAddr(%s) = %v, want %v", mapped, got, tt.want)
				}
				addr = mapped
			}
			if got := IsRestrictedAddr(addr.WithZone("test-zone")); got != tt.want {
				t.Fatalf("zoned IsRestrictedAddr(%s) = %v, want %v", addr, got, tt.want)
			}
		})
	}
}

func TestIsRestrictedAddrRejectsInvalidAddress(t *testing.T) {
	if !IsRestrictedAddr(netip.Addr{}) {
		t.Fatal("invalid address must be restricted")
	}
}

func TestGuardedDialContextRejectsSpecialPurposeAddresses(t *testing.T) {
	for _, address := range []string{
		"100.64.0.1", "::ffff:100.64.0.1", "192.0.0.8", "192.0.2.1", "198.18.0.1", "240.0.0.1",
		"64:ff9b::a00:1", "2001:db8::1", "2001:db8::1%test-zone", "2001:2::1", "3fff::1", "5f00::1",
	} {
		t.Run(address, func(t *testing.T) {
			for _, literal := range []bool{true, false} {
				resolverCalls := 0
				dial := GuardedDialContext(
					func(context.Context, string, string) ([]netip.Addr, error) {
						resolverCalls++
						return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(address)}, nil
					},
					func(addr netip.Addr) bool { return !IsRestrictedAddr(addr) },
				)
				host := "provider.example"
				if literal {
					host = address
				}
				// Cancellation prevents network access if address validation regresses.
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				conn, err := dial(ctx, "tcp", net.JoinHostPort(host, "443"))
				if conn != nil {
					_ = conn.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "endpoint address is not allowed") {
					t.Fatalf("literal=%t: error = %v, want disallowed address", literal, err)
				}
				if (resolverCalls == 0) != literal {
					t.Fatalf("literal=%t: resolver calls = %d", literal, resolverCalls)
				}
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
			netip.MustParseAddr("8.8.8.8"),
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
