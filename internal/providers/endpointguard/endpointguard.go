// Package endpointguard contains shared host classification helpers for
// operator-configured provider endpoints.
package endpointguard

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"
)

const DefaultResolutionTimeout = 5 * time.Second

type Resolver func(context.Context, string, string) ([]netip.Addr, error)

func LookupNetIP(ctx context.Context, host string, resolver Resolver) ([]netip.Addr, error) {
	if resolver == nil {
		resolver = net.DefaultResolver.LookupNetIP
	}
	resolveCtx, cancel := context.WithTimeout(ctx, DefaultResolutionTimeout)
	defer cancel()
	return resolver(resolveCtx, "ip", host)
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

func IsRestrictedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}
