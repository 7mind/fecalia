package bind

import (
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
)

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
	// further bytes on top; they are NOT part of this fixed constant because they are
	// configurable. Instead, when obfuscation is enabled the sizing path subtracts the
	// maximum junk-prefix length (config.Amnezia.MaxJunkPrefix) from the effective path
	// MTU before InnerMTU — see device.tunMTU (static) and telemetry.PMTUDiscovery's
	// UsablePathMTU (dynamic discovery) — so a full-size obfuscated DATA datagram still
	// fits the path MTU (T225, D85 fix-direction 4).
	WGTransportOverhead = 16 + 16
)

// FECParityMTUPenalty is the extra bytes a full-size FEC PARITY frame occupies on the
// wire over a DATA frame carrying the same-size inner payload (T24). A parity frame is
// nonce + PARITY header + shard payload, where the shard payload is the codec's length
// prefix + the FEC outer-seq prefix + the largest coded inner payload; a data frame is
// nonce + DATA header + that same inner payload. So the delta is:
//
//	(ParityOverhead + ShardFramingOverhead + fecSeqPrefixLen) - DataOverhead  ==  5 bytes.
//
// When FEC is enabled the inner MTU is reduced by this penalty so a full-MTU DATA frame
// AND its group's PARITY frame both fit the path MTU — otherwise parity exceeds the
// path MTU by exactly this much and IP-fragments or is EMSGSIZE/PMTUD-blackholed, which
// silently kills the very redundancy FEC exists to provide on bulk full-size traffic.
const FECParityMTUPenalty = frame.ParityOverhead + fec.ShardFramingOverhead + fecSeqPrefixLen - frame.DataOverhead

// InnerMTU returns the largest inner (TUN) MTU that avoids IP fragmentation over
// an IPv4 underlay of the given path MTU: pathMTU minus the IP+UDP, outer DATA
// frame, and WireGuard transport overheads. fecEnabled additionally reserves the
// parity-over-data delta so a full-size parity frame also fits (see
// FECParityMTUPenalty); pass false when FEC is off to keep the pre-T24 budget.
func InnerMTU(pathMTU int, fecEnabled bool) int {
	return pathMTU - IPv4UDPOverhead - frame.DataOverhead - WGTransportOverhead - fecPenalty(fecEnabled)
}

// InnerMTU6 is InnerMTU for an IPv6 underlay.
func InnerMTU6(pathMTU int, fecEnabled bool) int {
	return pathMTU - IPv6UDPOverhead - frame.DataOverhead - WGTransportOverhead - fecPenalty(fecEnabled)
}

// fecPenalty is the inner-MTU reduction to reserve for FEC parity, or 0 when FEC is
// off.
func fecPenalty(fecEnabled bool) int {
	if fecEnabled {
		return FECParityMTUPenalty
	}
	return 0
}
