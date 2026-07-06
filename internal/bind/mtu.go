package bind

import "github.com/7mind/wanbond/internal/frame"

// MTU accounting for the bonded datapath.
//
// A tunnelled application packet is wrapped in four nested layers before it hits
// the wire, each adding fixed overhead:
//
//		[ IP | UDP | outer DATA frame | WG transport | inner IP payload ]
//		  \___ IPv4UDPOverhead ___/   \_DataOverhead_/\_WGTransportOverhead_/
//
//	  - IP + UDP: the underlay carrying our outer datagram.
//	  - outer DATA frame: the bonding codec's nonce + DATA header (frame.DataOverhead).
//	  - WG transport: WireGuard's own data-message header + Poly1305 tag.
//
// InnerMTU sizes the TUN so a full-MTU inner packet, once wrapped in all four
// layers, still fits the path MTU without IP fragmentation. Fragmentation must be
// avoided: a fragment lost on a lossy WAN drops the whole datagram, and PMTUD
// black holes (ICMP "fragmentation needed" filtered by a middlebox) silently stall
// the tunnel. See docs/p1-mtu.md for the derivation and the MSS-clamping guidance
// that keeps TCP inside this budget.
const (
	// DefaultPathMTU is the assumed underlay path MTU when none is measured. 1500
	// is the near-universal Ethernet/most-broadband figure; deployments on links
	// with a smaller MTU (some LTE/PPPoE uplinks) must lower it.
	DefaultPathMTU = 1500

	// IPv4UDPOverhead is the outer IPv4 (20) + UDP (8) header cost. IPv4 is the
	// conservative default here; an IPv6 underlay costs 20 bytes more (see
	// IPv6UDPOverhead) so sizing for IPv4 and running over IPv6 only wastes a few
	// bytes, never fragments.
	IPv4UDPOverhead = 20 + 8

	// IPv6UDPOverhead is the outer IPv6 (40) + UDP (8) header cost.
	IPv6UDPOverhead = 40 + 8

	// WGTransportOverhead is WireGuard's per-datagram data-message overhead: the
	// 16-byte transport header (msg type + reserved + receiver index + counter)
	// plus the 16-byte Poly1305 authentication tag. Amnezia junk PREFIXES add
	// further variable bytes on top; they are handled as best-effort headroom, not
	// subtracted here, because they are configurable and per-packet variable.
	WGTransportOverhead = 16 + 16
)

// InnerMTU returns the largest inner (TUN) MTU that avoids IP fragmentation over
// an IPv4 underlay of the given path MTU: pathMTU minus the IP+UDP, outer DATA
// frame, and WireGuard transport overheads.
func InnerMTU(pathMTU int) int {
	return pathMTU - IPv4UDPOverhead - frame.DataOverhead - WGTransportOverhead
}

// InnerMTU6 is InnerMTU for an IPv6 underlay.
func InnerMTU6(pathMTU int) int {
	return pathMTU - IPv6UDPOverhead - frame.DataOverhead - WGTransportOverhead
}
