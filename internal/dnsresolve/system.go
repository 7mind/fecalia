package dnsresolve

import (
	"context"
	"net"
	"net/netip"
	"time"
)

// SystemResolver resolves hostnames via the Go standard resolver (net.Resolver),
// requesting both A and AAAA records in one call (net.LookupNetIP-shape). The
// standard resolver does not expose record TTLs, so Lookup always returns
// ttlOk=false.
type SystemResolver struct {
	resolver *net.Resolver
}

var _ Resolver = (*SystemResolver)(nil)

// NewSystemResolver returns a Resolver backed by net.DefaultResolver.
func NewSystemResolver() *SystemResolver {
	return &SystemResolver{resolver: net.DefaultResolver}
}

// Lookup implements Resolver.
func (s *SystemResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	addrs, err := s.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, 0, false, err
	}
	return addrs, 0, false, nil
}
