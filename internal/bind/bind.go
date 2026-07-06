package bind

import (
	"net/netip"
	"sync/atomic"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

// The type aliases below isolate the embedded WireGuard engine's conn package to
// this one file. Swapping amneziawg-go for upstream wireguard-go (the API-drift
// hedge) touches only this import and these aliases — the conn.Bind / conn.Endpoint
// contracts are byte-identical between the two forks.
type (
	// Bind is the transport the WireGuard device drives; wanbond's bonding logic
	// lives in implementations of it.
	Bind = conn.Bind
	// Endpoint identifies a peer's transport address.
	Endpoint = conn.Endpoint
	// ReceiveFunc is a packet-receive callback returned by Bind.Open.
	ReceiveFunc = conn.ReceiveFunc
)

// udpEndpoint is a conn.Endpoint over a destination AddrPort with an optional
// learned source IP. In the multipath Bind it serves as the SINGLE stable
// virtual endpoint presented to the engine per peer: the engine holds exactly
// one of these while the Bind privately fans out across the per-path sockets
// beneath it (see docs/p0-findings.md §3). The engine must never see per-packet
// endpoint churn, so every ReceiveFunc returns the very same *udpEndpoint.
//
// The destination is stored in an atomic.Pointer because the Bind pins it (once,
// under its mutex, from a receive goroutine) while the WireGuard engine reads it
// LOCKLESSLY from its own goroutines via the Dst* accessors — a data race on a
// plain field. The atomic makes writer and reader consistent per the Go memory
// model; a nil pointer means "destination not yet known" (equivalent to the zero
// AddrPort the accessors previously exposed).
type udpEndpoint struct {
	dst atomic.Pointer[netip.AddrPort]
	src netip.Addr
}

// setDst atomically publishes the destination to the lockless Dst* readers.
func (e *udpEndpoint) setDst(ap netip.AddrPort) { e.dst.Store(&ap) }

// dstValid reports whether a destination has been pinned.
func (e *udpEndpoint) dstValid() bool { return e.dst.Load() != nil }

// dstAddrPort returns the pinned destination, or the zero AddrPort if unset.
func (e *udpEndpoint) dstAddrPort() netip.AddrPort {
	if p := e.dst.Load(); p != nil {
		return *p
	}
	return netip.AddrPort{}
}

func (e *udpEndpoint) ClearSrc()           { e.src = netip.Addr{} }
func (e *udpEndpoint) DstToString() string { return e.dstAddrPort().String() }
func (e *udpEndpoint) DstIP() netip.Addr   { return e.dstAddrPort().Addr() }
func (e *udpEndpoint) SrcIP() netip.Addr   { return e.src }

func (e *udpEndpoint) SrcToString() string {
	if e.src.IsValid() {
		return e.src.String()
	}
	return ""
}

// DstToBytes serializes the destination as address bytes followed by the
// little-endian port, matching the engine's expectation for mac2 cookies.
func (e *udpEndpoint) DstToBytes() []byte {
	ap := e.dstAddrPort()
	b, _ := ap.Addr().MarshalBinary()
	port := ap.Port()
	return append(b, byte(port), byte(port>>8))
}
