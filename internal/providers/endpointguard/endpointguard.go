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

func IsRestrictedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}
