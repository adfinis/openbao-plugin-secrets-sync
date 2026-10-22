// Package endpointguard contains shared host classification helpers for
// operator-configured provider endpoints.
package endpointguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

const DefaultResolutionTimeout = 5 * time.Second

type Resolver func(context.Context, string, string) ([]netip.Addr, error)

// AddrAllowed reports whether a resolved endpoint address may be dialed.
type AddrAllowed func(netip.Addr) bool

func LookupNetIP(ctx context.Context, host string, resolver Resolver) ([]netip.Addr, error) {
	if resolver == nil {
		resolver = net.DefaultResolver.LookupNetIP
	}
	resolveCtx, cancel := context.WithTimeout(ctx, DefaultResolutionTimeout)
	defer cancel()
	return resolver(resolveCtx, "ip", host)
}

// GuardedDialContext resolves a hostname, validates the complete answer, and
// dials an approved IP address directly. Resolving and dialing in one function
// prevents a second DNS lookup from bypassing the address policy.
func GuardedDialContext(
	resolver Resolver,
	allowed AddrAllowed,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split endpoint address: %w", err)
		}
		addrs, err := addressesForDial(ctx, host, resolver)
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("endpoint DNS lookup returned no addresses for %q", host)
		}
		for _, addr := range addrs {
			if allowed == nil || !allowed(addr.Unmap()) {
				return nil, fmt.Errorf("endpoint address is not allowed: %s", addr)
			}
		}

		dialErrors := make([]error, 0, len(addrs))
		for _, addr := range addrs {
			if !networkSupportsAddr(network, addr) {
				continue
			}
			target := net.JoinHostPort(addr.String(), port)
			conn, dialErr := (&net.Dialer{}).DialContext(ctx, network, target)
			if dialErr == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, fmt.Errorf("dial %s: %w", target, dialErr))
		}
		if len(dialErrors) == 0 {
			return nil, fmt.Errorf("endpoint DNS lookup returned no addresses for network %q", network)
		}
		return nil, errors.Join(dialErrors...)
	}
}

func addressesForDial(ctx context.Context, host string, resolver Resolver) ([]netip.Addr, error) {
	if addr, ok := ParseAddr(host); ok {
		return []netip.Addr{addr}, nil
	}
	addrs, err := LookupNetIP(ctx, host, resolver)
	if err != nil {
		return nil, fmt.Errorf("resolve endpoint address: %w", err)
	}
	normalized := make([]netip.Addr, len(addrs))
	for i, addr := range addrs {
		normalized[i] = addr.Unmap()
	}
	return normalized, nil
}

func networkSupportsAddr(network string, addr netip.Addr) bool {
	switch network {
	case "tcp4":
		return addr.Is4()
	case "tcp6":
		return addr.Is6()
	default:
		return true
	}
}

func NormalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func IsLocalName(host string) bool {
	normalized := NormalizeHost(host)
	return normalized == "localhost" || strings.HasSuffix(normalized, ".localhost")
}

func IsLocalHost(host string) bool {
	if IsLocalName(host) {
		return true
	}
	addr, ok := ParseAddr(host)
	return ok && addr.IsLoopback()
}

func ParseAddr(host string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(NormalizeHost(host))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func IsRestrictedHost(host string) bool {
	if IsLocalName(host) {
		return true
	}
	addr, ok := ParseAddr(host)
	return ok && IsRestrictedAddr(addr)
}

// specialPurposePrefixes supplements IsGlobalUnicast and IsPrivate with the
// IANA IPv4 and IPv6 special-purpose registries, reviewed on 2026-10-22:
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
// Entries without Globally Reachable=True are restricted. More specific
// globally reachable assignments override their parent reservation.
// Deprecated IPv4-compatible and site-local IPv6 addresses are also restricted
// (RFC 4291 section 2.5.5.1 and RFC 3879).
var specialPurposePrefixes = []struct {
	prefix     netip.Prefix
	restricted bool
}{
	{netip.MustParsePrefix("0.0.0.0/8"), true},        // This network.
	{netip.MustParsePrefix("100.64.0.0/10"), true},    // Shared address space.
	{netip.MustParsePrefix("192.0.0.0/24"), true},     // IETF protocol assignments.
	{netip.MustParsePrefix("192.0.0.9/32"), false},    // PCP anycast.
	{netip.MustParsePrefix("192.0.0.10/32"), false},   // TURN anycast.
	{netip.MustParsePrefix("192.0.2.0/24"), true},     // Documentation.
	{netip.MustParsePrefix("192.88.99.0/24"), true},   // Deprecated 6to4 relay.
	{netip.MustParsePrefix("198.18.0.0/15"), true},    // Benchmarking.
	{netip.MustParsePrefix("198.51.100.0/24"), true},  // Documentation.
	{netip.MustParsePrefix("203.0.113.0/24"), true},   // Documentation.
	{netip.MustParsePrefix("240.0.0.0/4"), true},      // Reserved, including broadcast.
	{netip.MustParsePrefix("::/96"), true},            // Deprecated IPv4-compatible addresses.
	{netip.MustParsePrefix("64:ff9b:1::/48"), true},   // Local-use IPv4/IPv6 translation.
	{netip.MustParsePrefix("100::/64"), true},         // Discard-only.
	{netip.MustParsePrefix("100:0:0:1::/64"), true},   // Dummy IPv6 prefix.
	{netip.MustParsePrefix("2001::/23"), true},        // IETF assignments, including Teredo (N/A).
	{netip.MustParsePrefix("2001:1::1/128"), false},   // PCP anycast.
	{netip.MustParsePrefix("2001:1::2/128"), false},   // TURN anycast.
	{netip.MustParsePrefix("2001:1::3/128"), false},   // DNS-SD service registration anycast.
	{netip.MustParsePrefix("2001:3::/32"), false},     // AMT.
	{netip.MustParsePrefix("2001:4:112::/48"), false}, // AS112-v6.
	{netip.MustParsePrefix("2001:20::/28"), false},    // ORCHIDv2.
	{netip.MustParsePrefix("2001:30::/28"), false},    // Drone remote ID tags.
	{netip.MustParsePrefix("2001:db8::/32"), true},    // Documentation.
	{netip.MustParsePrefix("2002::/16"), true},        // 6to4 (N/A).
	{netip.MustParsePrefix("3fff::/20"), true},        // Documentation.
	{netip.MustParsePrefix("5f00::/16"), true},        // Segment routing SIDs.
	{netip.MustParsePrefix("fec0::/10"), true},        // Deprecated site-local addresses.
}

var wellKnownNAT64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// IsRestrictedAddr applies the default GitLab and token-auth Kubernetes address
// policy. It classifies addresses, not the deployment's routes or translators.
func IsRestrictedAddr(addr netip.Addr) bool {
	// Prefix.Contains rejects zoned addresses, so remove the zone for
	// classification. GuardedDialContext retains it when dialing the address.
	addr = addr.Unmap().WithZone("")
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return true
	}
	if wellKnownNAT64Prefix.Contains(addr) {
		// RFC 6052 section 3.1 forbids non-global IPv4 destinations in this
		// prefix. Apply the same policy to its embedded IPv4 address.
		raw := addr.As16()
		return IsRestrictedAddr(netip.AddrFrom4([4]byte{raw[12], raw[13], raw[14], raw[15]}))
	}
	restricted, longestMatch := false, -1
	for _, rule := range specialPurposePrefixes {
		if rule.prefix.Bits() > longestMatch && rule.prefix.Contains(addr) {
			restricted, longestMatch = rule.restricted, rule.prefix.Bits()
		}
	}
	return restricted
}
