package dnsresolve

import (
	"context"
	"net/netip"
	"time"
)

// Resolver resolves a hostname to its full A+AAAA record set, context-bounded.
// Implementations return addrs deterministically (both v4 and v6 when present);
// the caller filters/orders by local path family (device.orderAddrPorts drops
// addrs of a family no local path can source and puts v4 ahead of v6).
//
// minTTL is the minimum TTL across the returned records when the transport
// exposes it; ttlOk reports whether minTTL is meaningful — it is false when the
// transport discards TTL information (e.g. Go's standard net.Resolver never
// exposes it).
type Resolver interface {
	Lookup(ctx context.Context, host string) (addrs []netip.Addr, minTTL time.Duration, ttlOk bool, err error)
}
