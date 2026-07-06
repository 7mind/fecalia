package bind

import (
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestInnerMTUFixture pins the inner-MTU arithmetic to a fixture so a change to
// any overhead term is caught. For a 1500-byte IPv4 path:
//
//	1500 − (20+8 IP/UDP) − 39 outer DATA frame − (16+16 WG) = 1401.
func TestInnerMTUFixture(t *testing.T) {
	if frame.DataOverhead != 39 {
		t.Fatalf("frame.DataOverhead = %d, want 39 (nonce 24 + kind 1 + seq 8 + path 1 + group 4 + flags 1)", frame.DataOverhead)
	}

	const (
		pathMTU = 1500
		// 1500 - 28 - 39 - 32
		wantInner = 1401
	)
	if got := InnerMTU(pathMTU); got != wantInner {
		t.Fatalf("InnerMTU(%d) = %d, want %d", pathMTU, got, wantInner)
	}

	// Cross-check: the sum of all overheads plus the inner MTU must equal the
	// path MTU exactly (no slack, no fragmentation).
	overhead := IPv4UDPOverhead + frame.DataOverhead + WGTransportOverhead
	if InnerMTU(pathMTU)+overhead != pathMTU {
		t.Fatalf("inner (%d) + overhead (%d) = %d, want path MTU %d",
			InnerMTU(pathMTU), overhead, InnerMTU(pathMTU)+overhead, pathMTU)
	}

	// IPv6 underlay costs 20 bytes more of IP header.
	if got := InnerMTU6(pathMTU); got != wantInner-20 {
		t.Fatalf("InnerMTU6(%d) = %d, want %d", pathMTU, got, wantInner-20)
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
