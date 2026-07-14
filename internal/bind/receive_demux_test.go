package bind

import (
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestReceiveDemuxPerPeerResequencerAndEndpoint is the T86 acceptance: with TWO peerStates
// bound over the same shared socket, interleaved DATA for the two peers is delivered up by
// the SINGLE engine-facing drainer with per-packet endpoints matching EACH peer, and each
// peer's resequencer orders ITS OWN outer-seq stream independently (no cross-peer frame is
// ever observed in either resequencer). It exercises the receive/delivery path only: the
// per-(peer,path) ingestion demux (handleInbound -> ps.peer.resequencer) is T83's; the
// drainer fanning each peer's in-order releases up under that peer's virt is T86's.
func TestReceiveDemuxPerPeerResequencerAndEndpoint(t *testing.T) {
	psk := testKey(t, 0x42)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk) // one shared path "a"
	recvs, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(recvs) != 1 {
		t.Fatalf("Open returned %d ReceiveFuncs, want the single fan-in drainer", len(recvs))
	}
	recv := recvs[0]

	primary := m.peerState
	second := bindSecondPeer(t, m, "peer-2", psk, clk)

	primaryPath := m.paths[0]
	secondPath := peerPathByName(second, "a")
	if secondPath == nil {
		t.Fatalf("second peer has no view of shared path 'a': %v", pathNamesOfPeer(second))
	}
	// Distinct virtual endpoints per peer (invariant A1) — the property the delivery must
	// preserve when it stamps each frame.
	if primary.virt == second.virt {
		t.Fatal("the two peers share ONE virtual endpoint — per-peer endpoint fill is impossible")
	}

	srcA := netip.MustParseAddrPort("192.0.2.10:4000")
	srcB := netip.MustParseAddrPort("198.51.100.20:5000")

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build feed codec: %v", err)
	}
	feed := func(path *peerPathState, seq uint64, payload string, src netip.AddrPort) {
		t.Helper()
		raw, encErr := codec.Encode(nil, frame.Data{OuterSeq: seq, PathID: path.id, Payload: []byte(payload)})
		if encErr != nil {
			t.Fatalf("encode DATA seq=%d: %v", seq, encErr)
		}
		m.handleInbound(path, raw, src)
	}

	// Interleave the two peers' arrivals AND deliver each peer's own stream OUT OF ORDER
	// (seq 0, then 2, then 1) so a correct result proves each resequencer reorders its OWN
	// stream to 0,1,2 independently — a shared/leaky resequencer would interleave or reorder
	// across peers.
	feed(primaryPath, 0, "a0", srcA)
	feed(secondPath, 0, "b0", srcB)
	feed(primaryPath, 2, "a2", srcA)
	feed(secondPath, 2, "b2", srcB)
	feed(primaryPath, 1, "a1", srcA)
	feed(secondPath, 1, "b1", srcB)

	// Drain exactly the six buffered frames (each Pop is ready, so no call parks).
	type delivered struct {
		payload string
		ep      Endpoint
	}
	const wantN = 6
	got := make([]delivered, 0, wantN)
	for i := 0; i < wantN; i++ {
		pkt := make([]byte, 1500)
		packets := [][]byte{pkt}
		sizes := []int{0}
		eps := make([]Endpoint, 1)
		n, rerr := recv(packets, sizes, eps)
		if rerr != nil {
			t.Fatalf("receive %d: %v", i, rerr)
		}
		if n != 1 {
			t.Fatalf("receive %d returned n=%d, want 1", i, n)
		}
		got = append(got, delivered{payload: string(pkt[:sizes[0]]), ep: eps[0]})
	}

	// Split the delivered stream by the stamped endpoint. A frame stamped with primary.virt
	// MUST be one of primary's ("a") payloads and vice versa — that is the per-peer endpoint
	// attribution. Any mismatch is a cross-peer leak.
	var primaryStream, secondStream []string
	for i, d := range got {
		ep, ok := d.ep.(*udpEndpoint)
		if !ok {
			t.Fatalf("delivery %d endpoint is %T, want *udpEndpoint", i, d.ep)
		}
		switch ep {
		case primary.virt:
			if d.payload == "" || d.payload[0] != 'a' {
				t.Fatalf("delivery %d payload %q stamped with the PRIMARY endpoint — cross-peer leak", i, d.payload)
			}
			primaryStream = append(primaryStream, d.payload)
		case second.virt:
			if d.payload == "" || d.payload[0] != 'b' {
				t.Fatalf("delivery %d payload %q stamped with the SECOND-peer endpoint — cross-peer leak", i, d.payload)
			}
			secondStream = append(secondStream, d.payload)
		default:
			t.Fatalf("delivery %d stamped with an endpoint belonging to NEITHER bound peer", i)
		}
	}

	// Each peer's stream is delivered in its OWN outer-seq order (0,1,2), proving each
	// resequencer ordered its own stream independently despite the out-of-order arrival.
	assertOrdered := func(who string, stream []string, want []string) {
		t.Helper()
		if len(stream) != len(want) {
			t.Fatalf("%s peer delivered %d frames %v, want %v", who, len(stream), stream, want)
		}
		for i := range want {
			if stream[i] != want[i] {
				t.Fatalf("%s peer stream = %v, want %v (out of per-peer seq order)", who, stream, want)
			}
		}
	}
	assertOrdered("primary", primaryStream, []string{"a0", "a1", "a2"})
	assertOrdered("second", secondStream, []string{"b0", "b1", "b2"})

	// The per-peer virtual endpoints pinned to each peer's OWN learned source (never the
	// other's) — the return-path attribution Send routes replies on.
	if dst := primary.virt.dstAddrPort(); dst != srcA {
		t.Fatalf("primary virt pinned to %v, want %v (this peer's source)", dst, srcA)
	}
	if dst := second.virt.dstAddrPort(); dst != srcB {
		t.Fatalf("second-peer virt pinned to %v, want %v (this peer's source)", dst, srcB)
	}
}
