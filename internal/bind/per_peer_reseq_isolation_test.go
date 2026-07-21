package bind

import (
	"fmt"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestPerPeerReseqIsolation is the T95 acceptance: with TWO concentrator peers bound over the
// SAME shared socket via demuxInbound's source-demux (peerBySource) — the actual production
// routing path, not a hand-picked view — two interleaved outer-seq streams that OVERLAP in
// numeric seq space (both peers emit 0..5) and each arrive OUT OF ORDER stay fully separated:
// each peer's resequencer releases only its own payloads in strictly ascending order, and
// neither records a single suspect/late/duplicate drop caused by the other peer's traffic. A
// resequencer instance SHARED across peers would treat peer B's seq 0 as a duplicate/late
// re-delivery of peer A's already-released seq 0 (same numeric seq, different stream) — so this
// overlapping-seq design is what makes the test discriminate a broken (shared) demux from the
// correct (per-peer) one, rather than merely observing that two payloads happened not to
// collide.
func TestPerPeerReseqIsolation(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // second peer
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	secondView := peerPathByName(second, "a")

	dataCodecA, err := frame.NewCodec(pskA)
	if err != nil {
		t.Fatalf("build peer A data codec: %v", err)
	}
	dataCodecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B data codec: %v", err)
	}

	srcA := netip.MustParseAddrPort("203.0.113.1:51820")
	srcB := netip.MustParseAddrPort("198.51.100.7:51820")

	// Bind each source to its own peer through an authenticated PROBE — the same production
	// binding demuxInbound requires before it will route DATA by learned source.
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

	// Two streams over the SAME outer-seq numbers 0..5, each delivered OUT OF ORDER (0,2,1,4,3,5)
	// within its own stream, and INTERLEAVED with the other peer's arrivals frame-by-frame. Each
	// resequencer must reorder its OWN stream to 0,1,2,3,4,5 despite the interleave.
	//
	// The early (out-of-order) frames — seqs 2 and 4, which arrive ahead of their predecessors —
	// are stamped with a SECOND sender path id: since the D93 single-path immediate release
	// (T240/T241) a gap on ONE delivering path is treated as genuine loss and skipped with ~0
	// hold, so same-path out-of-order repair is no longer the contract. Stamping the early
	// frames as arriving over a second sender WAN (the same-socket concentrator-uplink
	// topology, review R250) makes each stream's reorder a CROSS-PATH one, which the
	// resequencer still holds for and repairs when the missing seq lands.
	arrival := []uint64{0, 2, 1, 4, 3, 5}
	earlyArrivals := map[uint64]bool{2: true, 4: true}
	for _, seq := range arrival {
		pidA, pidB := m.paths[0].id, secondView.id
		if earlyArrivals[seq] {
			pidA, pidB = pidA+1, pidB+1
		}
		m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecA, seq, pidA, fmt.Sprintf("A-%d", seq)), srcA)
		m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, seq, pidB, fmt.Sprintf("B-%d", seq)), srcB)
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

	wantA := []string{"A-0", "A-1", "A-2", "A-3", "A-4", "A-5"}
	wantB := []string{"B-0", "B-1", "B-2", "B-3", "B-4", "B-5"}

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
	// numeric-seq overlap with the OTHER peer never touched this peer's classification. A shared
	// resequencer would register peer B's seq 0..2 as duplicates/late (peer A's identical seqs
	// already released) or vice versa depending on arrival order.
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
