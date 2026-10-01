package bind

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
)

// TestInnerMTUFixture pins the inner-MTU arithmetic to a fixture so a change to
// any overhead term is caught. For a 1500-byte IPv4 path:
//
//	1500 − (20+8 IP/UDP) − 101 transport data frame − (16+16 WG) = 1339.
func TestInnerMTUFixture(t *testing.T) {
	if bond.Overhead != 101 {
		t.Fatalf("bond.Overhead = %d, want 101 (CONTROL framing 50 + lane header 19 + destination epoch 16 + sequence 8 + order 8)", bond.Overhead)
	}

	const (
		pathMTU = 1500
		// 1500 - 28 - 101 - 32
		wantInner = 1339
	)
	if got := InnerMTU(pathMTU); got != wantInner {
		t.Fatalf("InnerMTU(%d) = %d, want %d", pathMTU, got, wantInner)
	}

	// Cross-check: the sum of all overheads plus the inner MTU must equal the
	// path MTU exactly (no slack, no fragmentation).
	overhead := IPv4UDPOverhead + bond.Overhead + WGTransportOverhead
	if InnerMTU(pathMTU)+overhead != pathMTU {
		t.Fatalf("inner (%d) + overhead (%d) = %d, want path MTU %d",
			InnerMTU(pathMTU), overhead, InnerMTU(pathMTU)+overhead, pathMTU)
	}

	// IPv6 underlay costs 20 bytes more of IP header.
	if got := InnerMTU6(pathMTU); got != wantInner-20 {
		t.Fatalf("InnerMTU6(%d) = %d, want %d", pathMTU, got, wantInner-20)
	}
}

// TestPathMTUOverheadMatchesConfigMirror keeps internal/config's mirrored
// outerPathOverheadBytes constant (T200, D85) honest against this package's real
// overhead figures. config cannot import internal/bind (bind imports config;
// importing back would cycle), so config's copy is a hand-mirrored literal (161)
// rather than a reference to these constants — if any of the three terms below ever
// changes, this test fails here first, and the config.go mirror must be updated in
// lockstep.
func TestPathMTUOverheadMatchesConfigMirror(t *testing.T) {
	const wantMirroredInConfig = 161 // internal/config/config.go: outerPathOverheadBytes
	if got := IPv4UDPOverhead + bond.Overhead + WGTransportOverhead; got != wantMirroredInConfig {
		t.Fatalf("IPv4UDPOverhead(%d) + bond.Overhead(%d) + WGTransportOverhead(%d) = %d, want %d (must match internal/config's outerPathOverheadBytes mirror)",
			IPv4UDPOverhead, bond.Overhead, WGTransportOverhead, got, wantMirroredInConfig)
	}
}

// TestTransportOverheadMatchesEncoding confirms bond.Overhead equals the real wire
// cost the transport and the codec add to a datagram, so the MTU budget can never
// silently drift from them: the largest WireGuard datagram the inner MTU admits must
// fill the path MTU exactly once the transport has framed it.
func TestTransportOverheadMatchesEncoding(t *testing.T) {
	const pathMTU = 1500
	psk := testKey(t, 0x01)
	now := time.Unix(100, 0)
	sender := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	sender.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
	sender.Path(0, 0, 30*time.Millisecond, now)

	// The TUN's inner IP packet plus WireGuard's own transport overhead.
	payload := make([]byte, InnerMTU(pathMTU)+WGTransportOverhead)
	if err := sender.Enqueue(payload, bond.PacketMetadata{}, now); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	sent := sender.Poll(now)
	if len(sent) != 1 {
		t.Fatalf("transport sent %d frames for one datagram, want 1", len(sent))
	}
	raw, err := frame.Encode(psk, sent[0].Frame)
	if err != nil {
		t.Fatalf("encode data frame: %v", err)
	}
	if got := len(raw) - len(payload); got != bond.Overhead {
		t.Fatalf("a data frame adds %d bytes on the wire, but bond.Overhead = %d", got, bond.Overhead)
	}
	if got := len(raw) + IPv4UDPOverhead; got != pathMTU {
		t.Fatalf("full-size data frame on-wire = %d (+IP/UDP), want exactly the path MTU %d", got, pathMTU)
	}
}
