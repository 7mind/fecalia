package dnsresolve

import (
	"context"
	"fmt"
	"net/netip"
	"time"
)

// FakeResolver is a hand-written in-memory Resolver backed by a static
// host->addrs map (the dual-tests dummy for internal/dnsresolve): unit tests
// across the codebase inject it in place of SystemResolver.
type FakeResolver struct {
	// Hosts maps a hostname to the addrs Lookup returns for it, in order.
	Hosts map[string][]netip.Addr
	// MinTTL and TTLOk are returned verbatim for every mapped host.
	MinTTL time.Duration
	TTLOk  bool
}

var _ Resolver = (*FakeResolver)(nil)

// Lookup implements Resolver: it returns the addrs mapped to host, or a
// non-nil error when host is not in Hosts.
func (f *FakeResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	addrs, ok := f.Hosts[host]
	if !ok {
		return nil, 0, false, fmt.Errorf("dnsresolve: no such host %q", host)
	}
	return addrs, f.MinTTL, f.TTLOk, nil
}
