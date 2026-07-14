package bind

import (
	"fmt"
	"net/netip"
	"testing"
)

// TestPerPeerResequencerLifecycle is the T87 acceptance: with TWO peerStates bound over the
// same shared sockets, each peer's receive resequencer is an INDEPENDENT release plane. Two
// interleaved outer-seq streams stay separated (each peer's Pop yields only its own stream),
// a Close→Open cycle re-builds EVERY bound peer's resequencer + send Codec fresh (not just
// the primary's via promotion — the symmetry with closeSocketsLocked that a concentrator peer
// relies on across a reconnect), and a Rebaseline triggered on peer A (via the edge hub-
// failover SetPeerRemote) leaves peer B's release point untouched — the D32-class regression,
// now proven per-peer.
func TestPerPeerResequencerLifecycle(t *testing.T) {
	psk := testKey(t, 0x71)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	primary := m.peerState
	second := bindSecondPeer(t, m, "peer-2", psk, clk)

	srcA := netip.MustParseAddrPort("203.0.113.1:51820")
	srcB := netip.MustParseAddrPort("198.51.100.7:51820")

	// --- Two interleaved streams stay separated. ---
	// Feed peer A and peer B the SAME outer-seq positions with DISTINCT payloads, interleaved.
	// Each peer's resequencer must release ONLY its own payloads, in order — a shared/global
	// buffer would cross the streams.
	stream := []uint64{100, 101, 102}
	for _, seq := range stream {
		primary.resequencer.Load().Observe(seq, []byte(fmt.Sprintf("A-%d", seq)), srcA)
		second.resequencer.Load().Observe(seq, []byte(fmt.Sprintf("B-%d", seq)), srcB)
	}
	for _, seq := range stream {
		gotA, okA := primary.resequencer.Load().Pop()
		if wantA := fmt.Sprintf("A-%d", seq); !okA || string(gotA.Payload) != wantA {
			t.Fatalf("peer A Pop = %q (ok=%v), want %q (streams crossed)", gotA.Payload, okA, wantA)
		}
		if gotA.Src != srcA {
			t.Fatalf("peer A item src = %v, want %v", gotA.Src, srcA)
		}
		gotB, okB := second.resequencer.Load().Pop()
		if wantB := fmt.Sprintf("B-%d", seq); !okB || string(gotB.Payload) != wantB {
			t.Fatalf("peer B Pop = %q (ok=%v), want %q (streams crossed)", gotB.Payload, okB, wantB)
		}
		if gotB.Src != srcB {
			t.Fatalf("peer B item src = %v, want %v", gotB.Src, srcB)
		}
	}

	// --- The separation survives a Close→Open cycle: EVERY bound peer's datapath is re-built. ---
	primaryRQ1 := primary.resequencer.Load()
	secondRQ1 := second.resequencer.Load()
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close clears every peer's per-Open state; the second peer's send Codec must be nil now.
	if second.sendCodec != nil {
		t.Fatalf("second peer sendCodec not cleared by Close")
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	primaryRQ2 := primary.resequencer.Load()
	secondRQ2 := second.resequencer.Load()
	// Both peers got a FRESH resequencer (a distinct instance) on re-Open — without the per-peer
	// rebuild the second peer would keep its stale instance (or a nil send Codec).
	if primaryRQ2 == nil || primaryRQ2 == primaryRQ1 {
		t.Fatalf("primary resequencer not re-built on re-Open (fresh=%v)", primaryRQ2 != primaryRQ1)
	}
	if secondRQ2 == nil || secondRQ2 == secondRQ1 {
		t.Fatalf("second peer resequencer not re-built on re-Open (Open only rebuilt the primary): fresh=%v", secondRQ2 != secondRQ1)
	}
	if second.sendCodec == nil {
		t.Fatalf("second peer sendCodec not re-built on re-Open (Open only rebuilt the primary)")
	}

	// --- Rebaseline on peer A leaves peer B's release point untouched (D32, per-peer). ---
	// Advance BOTH peers' release point to a high outer-seq (the prior hub's high-rate stream).
	const high = uint64(10000)
	for _, ps := range []*peerState{primary, second} {
		ps.resequencer.Load().Observe(high, []byte("high"), srcA)
		if _, ok := ps.resequencer.Load().Pop(); !ok {
			t.Fatalf("expected the high seq to release and advance next")
		}
	}

	// The edge hub-failover switch: SetPeerRemote re-baselines ONLY the primary (peer A).
	m.SetPeerRemote(netip.MustParseAddrPort("192.0.2.9:51820"))

	if got := primary.resequencer.Load().Stats().Rebaselines; got != 1 {
		t.Fatalf("primary Rebaselines = %d, want 1 (SetPeerRemote must re-baseline the switched peer)", got)
	}
	if got := second.resequencer.Load().Stats().Rebaselines; got != 0 {
		t.Fatalf("second peer Rebaselines = %d, want 0 (a hub switch on peer A must not touch peer B)", got)
	}

	// A LOW seq now: peer A (re-baselined) re-anchors on it and DELIVERS; peer B (release point
	// still at high+1) rejects it as a suspect out-of-band frame and delivers NOTHING — proof its
	// `next` was untouched by peer A's re-baseline.
	const low = uint64(1)
	primary.resequencer.Load().Observe(low, []byte("A-reanchor"), srcA)
	gotLow, okLow := primary.resequencer.Load().Pop()
	if !okLow || string(gotLow.Payload) != "A-reanchor" {
		t.Fatalf("peer A did not re-anchor on the low seq after re-baseline: got %q (ok=%v)", gotLow.Payload, okLow)
	}

	second.resequencer.Load().Observe(low, []byte("B-should-drop"), srcB)
	if it, ok := second.resequencer.Load().Pop(); ok {
		t.Fatalf("peer B delivered the low seq %q — its release point was disturbed by peer A's re-baseline", it.Payload)
	}
	if got := second.resequencer.Load().Stats().DroppedSuspect; got == 0 {
		t.Fatalf("peer B did not reject the low seq as suspect (its next was moved): DroppedSuspect=%d", got)
	}
}
