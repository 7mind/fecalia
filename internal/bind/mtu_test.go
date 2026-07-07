package bind

import (
	"testing"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
)

// TestInnerMTUFixture pins the inner-MTU arithmetic to a fixture so a change to
// any overhead term is caught. For a 1500-byte IPv4 path with FEC off:
//
//	1500 − (20+8 IP/UDP) − 40 outer DATA frame − (16+16 WG) = 1400.
func TestInnerMTUFixture(t *testing.T) {
	if frame.DataOverhead != 40 {
		t.Fatalf("frame.DataOverhead = %d, want 40 (nonce 24 + kind 1 + seq 8 + path 1 + group 4 + fec-index 1 + flags 1)", frame.DataOverhead)
	}

	const (
		pathMTU = 1500
		// 1500 - 28 - 40 - 32
		wantInner = 1400
	)
	if got := InnerMTU(pathMTU, false); got != wantInner {
		t.Fatalf("InnerMTU(%d, fec=false) = %d, want %d", pathMTU, got, wantInner)
	}

	// Cross-check: the sum of all overheads plus the inner MTU must equal the
	// path MTU exactly (no slack, no fragmentation).
	overhead := IPv4UDPOverhead + frame.DataOverhead + WGTransportOverhead
	if InnerMTU(pathMTU, false)+overhead != pathMTU {
		t.Fatalf("inner (%d) + overhead (%d) = %d, want path MTU %d",
			InnerMTU(pathMTU, false), overhead, InnerMTU(pathMTU, false)+overhead, pathMTU)
	}

	// IPv6 underlay costs 20 bytes more of IP header.
	if got := InnerMTU6(pathMTU, false); got != wantInner-20 {
		t.Fatalf("InnerMTU6(%d, fec=false) = %d, want %d", pathMTU, got, wantInner-20)
	}

	// With FEC on, the inner MTU is a further FECParityMTUPenalty smaller so a full-size
	// parity frame also fits (T24). The penalty is exactly 5 for this wire layout.
	if FECParityMTUPenalty != 5 {
		t.Fatalf("FECParityMTUPenalty = %d, want 5 (parity overhead + shard framing + seq prefix − data overhead)", FECParityMTUPenalty)
	}
	if got := InnerMTU(pathMTU, true); got != wantInner-FECParityMTUPenalty {
		t.Fatalf("InnerMTU(%d, fec=true) = %d, want %d", pathMTU, got, wantInner-FECParityMTUPenalty)
	}
}

// TestParityFrameFitsPathMTUWithFEC is the fix witness for the parity-exceeds-MTU
// defect: at the FEC-aware inner MTU, a full-size DATA frame's group PARITY frame must
// fit the path MTU on the wire. Under the pre-fix budget (which reserved nothing for
// parity) the parity frame exceeded the path MTU by exactly FECParityMTUPenalty and
// IP-fragmented / PMTUD-blackholed. The parity wire size is computed from the real
// codec + FEC shard geometry, not restated, so a framing change cannot pass vacuously.
func TestParityFrameFitsPathMTUWithFEC(t *testing.T) {
	const pathMTU = 1500
	inner := InnerMTU(pathMTU, true)

	// The largest inner (WG) payload a DATA frame carries at this inner MTU: the TUN's
	// inner IP packet plus WireGuard's own transport overhead.
	maxWGPayload := inner + WGTransportOverhead

	// The FEC data shard coded bytes are fecSeqPrefixLen + the WG payload; a parity
	// shard's wire payload is the codec's length prefix + that shard length. The parity
	// FRAME wire size is the PARITY outer framing + the parity shard payload.
	shardLen := fec.ShardFramingOverhead + fecSeqPrefixLen + maxWGPayload
	parityWire := frame.ParityOverhead + shardLen

	// The outer datagram (parity frame) plus IP/UDP must fit the path MTU.
	if got := parityWire + IPv4UDPOverhead; got > pathMTU {
		t.Fatalf("full-size PARITY on-wire = %d (+IP/UDP), exceeds path MTU %d by %d — parity would fragment", got, pathMTU, got-pathMTU)
	}

	// And a full-size DATA frame must still fit too (the penalty did not over-reserve).
	dataWire := frame.DataOverhead + maxWGPayload
	if got := dataWire + IPv4UDPOverhead; got > pathMTU {
		t.Fatalf("full-size DATA on-wire = %d (+IP/UDP), exceeds path MTU %d", got, pathMTU)
	}
}

// TestDataOverheadMatchesEncoding confirms DataOverhead equals the real wire cost
// of a zero-payload DATA frame, so the MTU budget can never silently drift from
// the codec.
func TestDataOverheadMatchesEncoding(t *testing.T) {
	psk := testKey(t, 0x01)
	raw, err := frame.Encode(psk, frame.Data{})
	if err != nil {
		t.Fatalf("encode empty DATA: %v", err)
	}
	if len(raw) != frame.DataOverhead {
		t.Fatalf("empty DATA frame is %d bytes on the wire, but frame.DataOverhead = %d", len(raw), frame.DataOverhead)
	}
}
