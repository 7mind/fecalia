// Package netutil holds small network host-classification helpers shared
// across packages that need to reason about listen addresses without
// depending on each other's internals (e.g. config validation and the
// metrics/monitor endpoints).
package netutil

import (
	"fmt"
	"net"
	"net/netip"
)

// IsLoopbackHost reports whether addr's host portion (a "host:port" string) is
// a loopback address. An IP literal is classified directly via
// netip.Addr.IsLoopback; a hostname is resolved and classified loopback only
// if EVERY resolved address is loopback, mirroring internal/metrics/server.go's
// requireLoopback host-classification. An empty host (e.g. ":9095") binds all
// interfaces and is classified non-loopback. It returns an error only for a
// syntactically invalid "host:port" or an unresolvable hostname — never for a
// merely non-loopback address, which callers classify from the returned bool.
func IsLoopbackHost(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if host == "" {
		return false, nil
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback(), nil
	}

	// Hostname: resolve and require every resolved address to be loopback.
	ips, err := net.LookupIP(host)
	if err != nil {
		return false, fmt.Errorf("cannot resolve listen host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return false, fmt.Errorf("listen host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}
