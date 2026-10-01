package bind

import (
	"fmt"
	"net/netip"
	"testing"
)

// TestPerPeerReseqIsolation is the T95 acceptance: with TWO concentrator peers bound over the
// SAME shared socket via demuxInbound's source-demux (peerBySource) — the actual production
// routing path, not a hand-picked view — two interleaved ordered streams that OVERLAP in
// numeric order space (both peers emit 1..6) and each arrive OUT OF ORDER stay fully separated:
// each peer's resequencer releases only its own payloads in strictly ascending order, and
// neither records a single suspect/late/duplicate drop caused by the other peer's traffic. A
// resequencer instance SHARED across peers would treat peer B's first datagram as a
// duplicate/late re-delivery of peer A's already-released first datagram (same numeric order,
// different stream) — so this overlapping-order design is what makes the test discriminate a broken (shared) demux from the
// correct (per-peer) one, rather than merely observing that two payloads happened not to
// collide.
func TestPerPeerReseqIsolation(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // second peer
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	secondView := peerPathByName(second, "a")

	srcA := netip.MustParseAddrPort("203.0.113.1:51820")
	srcB := netip.MustParseAddrPort("198.51.100.7:51820")

	// Bind each source to its own peer through an authenticated PROBE — the same production
	// binding demuxInbound requires before it will route data by learned source.
	m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), srcA)
	if bound, ok := m.lookupPeerBySource(srcA); !ok || bound != primary {
		t.Fatalf("srcA did not bind to peer A: bound=%v ok=%v", bound, ok)
	}
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), srcB)
	if bound, ok := m.lookupPeerBySource(srcB); !ok || bound != second {
		t.Fatalf("srcB did not bind to peer B: bound=%v ok=%v", bound, ok)
	}

	// Both peers' resequencer rings were lazily instantiated on their first binding.
	if primary.resequencer.Load() == nil {
		t.Fatal("peer A resequencer not instantiated after binding")
	}
	if second.resequencer.Load() == nil {
		t.Fatal("peer B resequencer not instantiated after binding")
	}

	// Two streams over the SAME delivery orders 1..6, each delivered OUT OF ORDER
	// (1,3,2,5,4,6) within its own stream, and INTERLEAVED with the other peer's arrivals
	// frame-by-frame. Each resequencer must reorder its OWN stream to 1..6 despite the
	// interleave.
	remoteA := newRemoteTransport(t, primary, 987)
	remoteA.join(m.paths[0], srcA)
	remoteB := newRemoteTransport(t, second, 988)
	remoteB.join(secondView, srcB)
	const datagrams = 6
	var framesA, framesB [][]byte
	for order := 1; order <= datagrams; order++ {
		framesA = append(framesA, remoteA.wire(remoteA.bulk(m.paths[0], []byte(fmt.Sprintf("A-%d", order)))))
		framesB = append(framesB, remoteB.wire(remoteB.bulk(secondView, []byte(fmt.Sprintf("B-%d", order)))))
	}
	for _, order := range []int{1, 3, 2, 5, 4, 6} {
		m.demuxInbound(m.paths[0], framesA[order-1], srcA)
		m.demuxInbound(m.paths[0], framesB[order-1], srcB)
	}

	popAll := func(who string, p *peerState) []string {
		t.Helper()
		var got []string
		for {
			it, ok := p.resequencer.Load().Pop()
			if !ok {
				break
			}
			got = append(got, string(it.Payload))
		}
		return got
	}

	gotA := popAll("A", primary)
	gotB := popAll("B", second)

	wantA := []string{"A-1", "A-2", "A-3", "A-4", "A-5", "A-6"}
	wantB := []string{"B-1", "B-2", "B-3", "B-4", "B-5", "B-6"}

	assertStream := func(who string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s peer released %d frames %v, want %v", who, len(got), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s peer stream = %v, want %v (streams crossed or reordered)", who, got, want)
			}
		}
	}
	assertStream("A", gotA, wantA)
	assertStream("B", gotB, wantB)

	// Neither peer's resequencer recorded ANY suspect/late/duplicate drop: the interleave and
	// numeric-order overlap with the OTHER peer never touched this peer's classification. A
	// shared resequencer would register peer B's first datagrams as duplicates/late (peer A's
	// identical orders already released) or vice versa depending on arrival order.
	assertClean := func(who string, p *peerState) {
		t.Helper()
		st := p.resequencer.Load().Stats()
		if st.DroppedSuspect != 0 {
			t.Fatalf("%s peer DroppedSuspect = %d, want 0 (cross-peer interleave caused a suspect drop)", who, st.DroppedSuspect)
		}
		if st.DroppedOld != 0 {
			t.Fatalf("%s peer DroppedOld = %d, want 0 (cross-peer interleave caused a late drop)", who, st.DroppedOld)
		}
		if st.DroppedDup != 0 {
			t.Fatalf("%s peer DroppedDup = %d, want 0 (cross-peer interleave caused a duplicate drop)", who, st.DroppedDup)
		}
	}
	assertClean("A", primary)
	assertClean("B", second)
}
