package bind

import (
	"fmt"
	"net/netip"
	"testing"
)

// TestPerPeerResequencerLifecycle is the T87 acceptance: with TWO peerStates bound over the
// same shared sockets, each peer's receive resequencer is an INDEPENDENT release plane. Two
// interleaved streams stay separated (each peer's Pop yields only its own stream),
// a Close→Open cycle re-builds EVERY bound peer's resequencer + transport fresh (not just
// the primary's via promotion — the symmetry with generation retirement that a concentrator peer
// relies on across a reconnect), and a re-baseline triggered on peer A (the edge hub failover:
// SetPeerRemote, then the standby hub's hello) leaves peer B's release point untouched — the
// D32-class regression, now proven per-peer.
func TestPerPeerResequencerLifecycle(t *testing.T) {
	psk := testKey(t, 0x71)
	clk := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	primary := m.peerState
	second := bindSecondPeer(t, m, "peer-2", psk, clk)

	srcA := netip.MustParseAddrPort("203.0.113.1:51820")
	srcB := netip.MustParseAddrPort("198.51.100.7:51820")

	// --- Two interleaved streams stay separated. ---
	// Feed peer A and peer B the SAME sequence positions with DISTINCT payloads, interleaved.
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
	// Close clears every peer's per-Open state; the second peer's transport must be gone now.
	if second.adaptive.Load() != nil {
		t.Fatalf("second peer transport not cleared by Close")
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	primaryRQ2 := primary.resequencer.Load()
	secondRQ2 := second.resequencer.Load()
	// Both peers got a FRESH resequencer (a distinct instance) on re-Open — without the per-peer
	// rebuild the second peer would keep its stale instance (or no transport).
	if primaryRQ2 == nil || primaryRQ2 == primaryRQ1 {
		t.Fatalf("primary resequencer not re-built on re-Open (fresh=%v)", primaryRQ2 != primaryRQ1)
	}
	if secondRQ2 == nil || secondRQ2 == secondRQ1 {
		t.Fatalf("second peer resequencer not re-built on re-Open (Open only rebuilt the primary): fresh=%v", secondRQ2 != secondRQ1)
	}
	if second.adaptive.Load() == nil {
		t.Fatalf("second peer transport not re-built on re-Open (Open only rebuilt the primary)")
	}

	// --- A re-baseline on peer A leaves peer B's release point untouched (D32, per-peer). ---
	// Advance BOTH peers' release point (the prior hub's stream).
	primaryView := primary.paths[0]
	secondView := peerPathByName(second, "a")
	hubA := newRemoteTransport(t, primary, 987)
	hubA.join(primaryView, srcA)
	hubB := newRemoteTransport(t, second, 988)
	hubB.join(secondView, srcB)
	for _, payload := range []string{"one", "two", "three"} {
		m.handleInbound(primaryView, hubA.wire(hubA.bulk(primaryView, []byte(payload))), srcA)
		if it, ok := primary.resequencer.Load().Pop(); !ok || string(it.Payload) != payload {
			t.Fatalf("peer A did not deliver %q: got %q (ok=%v)", payload, it.Payload, ok)
		}
		m.handleInbound(secondView, hubB.wire(hubB.bulk(secondView, []byte(payload))), srcB)
		if it, ok := second.resequencer.Load().Pop(); !ok || string(it.Payload) != payload {
			t.Fatalf("peer B did not deliver %q: got %q (ok=%v)", payload, it.Payload, ok)
		}
	}
	primaryBaseline := primary.resequencer.Load().Stats().Rebaselines
	secondBaseline := second.resequencer.Load().Stats().Rebaselines

	// The edge hub-failover switch: SetPeerRemote repoints ONLY the primary (peer A), whose
	// transport forgets its lanes to the prior hub; the standby hub is another process, and
	// its hello re-baselines peer A's resequencer.
	standby := netip.MustParseAddrPort("192.0.2.9:51820")
	m.SetPeerRemote(standby)
	m.handleInbound(primaryView, hubA.wire(hubA.bulk(primaryView, []byte("prior-hub-straggler"))), srcA)
	if it, ok := primary.resequencer.Load().Pop(); ok {
		t.Fatalf("peer A delivered a datagram of the prior hub after the switch (%q)", it.Payload)
	}
	standbyHub := newRemoteTransport(t, primary, 989)
	standbyHub.join(primaryView, standby)

	if got := primary.resequencer.Load().Stats().Rebaselines; got != primaryBaseline+1 {
		t.Fatalf("primary Rebaselines = %d, want %d (the standby hub's hello must re-baseline the switched peer)", got, primaryBaseline+1)
	}
	if got := second.resequencer.Load().Stats().Rebaselines; got != secondBaseline {
		t.Fatalf("second peer Rebaselines = %d, want %d (a hub switch on peer A must not touch peer B)", got, secondBaseline)
	}

	// The standby hub's FIRST datagram now: peer A (re-baselined) is anchored on it and
	// DELIVERS. Peer B's release point was untouched by peer A's re-baseline: its prior hub's
	// stream simply continues.
	m.handleInbound(primaryView, standbyHub.wire(standbyHub.bulk(primaryView, []byte("A-reanchor"))), standby)
	gotLow, okLow := primary.resequencer.Load().Pop()
	if !okLow || string(gotLow.Payload) != "A-reanchor" {
		t.Fatalf("peer A did not re-anchor on the standby hub's first datagram: got %q (ok=%v)", gotLow.Payload, okLow)
	}
	if gotLow.Src != standby {
		t.Fatalf("peer A's re-anchored item src = %v, want the standby hub %v", gotLow.Src, standby)
	}

	m.handleInbound(secondView, hubB.wire(hubB.bulk(secondView, []byte("B-continues"))), srcB)
	if it, ok := second.resequencer.Load().Pop(); !ok || string(it.Payload) != "B-continues" {
		t.Fatalf("peer B's stream did not continue — its release point was disturbed by peer A's re-baseline: got %q (ok=%v)", it.Payload, ok)
	}
	if got := second.resequencer.Load().Stats().DroppedSuspect; got != 0 {
		t.Fatalf("peer B recorded %d suspect drops across peer A's hub switch, want 0", got)
	}
}
