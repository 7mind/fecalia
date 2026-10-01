package bind

import "github.com/7mind/wanbond/internal/bond"

// MTU accounting for the bonded datapath.
//
// A tunnelled application packet is wrapped in four nested layers before it hits
// the wire, each adding fixed overhead:
//
//		[ IP | UDP | outer data frame | WG transport | inner IP payload ]
//		  \___ IPv4UDPOverhead ___/   \_bond.Overhead_/\_WGTransportOverhead_/
//
//	  - IP + UDP: the underlay carrying our outer datagram.
//	  - outer data frame: the bonding transport's authenticated envelope and lane
//	    header (bond.Overhead).
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
	// further bytes on top; they are NOT part of this fixed constant because they are
	// configurable. Instead, when obfuscation is enabled the sizing path subtracts the
	// maximum junk-prefix length (config.Amnezia.MaxJunkPrefix) from the effective path
	// MTU before InnerMTU — see device.tunMTU (static) and telemetry.PMTUDiscovery's
	// UsablePathMTU (dynamic discovery) — so a full-size obfuscated DATA datagram still
	// fits the path MTU (T225, D85 fix-direction 4).
	WGTransportOverhead = 16 + 16
)

// InnerMTU returns the largest inner (TUN) MTU that avoids IP fragmentation over
// an IPv4 underlay of the given path MTU: pathMTU minus the IP+UDP, outer data
// frame, and WireGuard transport overheads.
func InnerMTU(pathMTU int) int {
	return pathMTU - IPv4UDPOverhead - bond.Overhead - WGTransportOverhead
}

// InnerMTU6 is InnerMTU for an IPv6 underlay.
func InnerMTU6(pathMTU int) int {
	return pathMTU - IPv6UDPOverhead - bond.Overhead - WGTransportOverhead
}
