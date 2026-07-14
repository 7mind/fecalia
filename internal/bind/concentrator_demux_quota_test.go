package bind

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestConcentratorDemuxSharesOneIPByAddrPort is the D47 acceptance (a): two peers behind ONE
// public IP (CGNAT — one netip.Addr, distinct source ports) each bind INDEPENDENTLY, and each
// peer's DATA reaches its OWN resequencer. This discriminates the AddrPort re-key from the old
// bare-netip.Addr key: under the old key the two same-address sources would collide into a single
// slot (the second peer's PROBE re-pointing the first, T90 roam), cross-wiring the two peers.
func TestConcentratorDemuxSharesOneIPByAddrPort(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	clk := newFakeClock()

	m, primary, second := twoPeerConcentrator(t, pskA, pskB)
	aView := m.paths[0]
	bView := peerPathByName(second, "a")

	// One public IP (CGNAT range 100.64/10), two distinct source ports.
	const cgnatIP = "100.64.0.7"
	srcA := netip.MustParseAddrPort(cgnatIP + ":9001")
	srcB := netip.MustParseAddrPort(cgnatIP + ":9002")
	if srcA.Addr() != srcB.Addr() {
		t.Fatalf("test setup: the two sources must share ONE address (CGNAT), got %v vs %v", srcA.Addr(), srcB.Addr())
	}

	// Each peer authenticates from its own port behind the shared IP.
	m.demuxInbound(aView, authProbe(t, pskA, aView.id, 1, clk), srcA)
	m.demuxInbound(aView, authProbe(t, pskB, bView.id, 1, clk), srcB)

	if bound, ok := m.lookupPeerBySource(srcA); !ok || bound != primary {
		t.Fatalf("srcA bound to %v (ok=%v), want the primary", bound, ok)
	}
	if bound, ok := m.lookupPeerBySource(srcB); !ok || bound != second {
		t.Fatalf("srcB bound to %v (ok=%v), want peer B — the shared IP must NOT collide the two peers", bound, ok)
	}

	// Each peer's DATA reaches its OWN resequencer; neither leaks into the other's.
	dataCodecA, _ := frame.NewCodec(pskA)
	dataCodecB, _ := frame.NewCodec(pskB)
	m.demuxInbound(aView, mustEncodeData(t, dataCodecA, 10, aView.id, "a-data"), srcA)
	m.demuxInbound(aView, mustEncodeData(t, dataCodecB, 20, bView.id, "b-data"), srcB)

	if it, ok := primary.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("a-data")) {
		t.Fatalf("peer A DATA not delivered to peer A's resequencer: ok=%v payload=%q", ok, it.Payload)
	}
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("b-data")) {
		t.Fatalf("peer B DATA not delivered to peer B's resequencer: ok=%v payload=%q", ok, it.Payload)
	}
	if it, ok := primary.resequencer.Load().Pop(); ok {
		t.Fatalf("peer A's resequencer received an extra frame %q (leak across the shared IP)", it.Payload)
	}
	if it, ok := second.resequencer.Load().Pop(); ok {
		t.Fatalf("peer B's resequencer received an extra frame %q (leak across the shared IP)", it.Payload)
	}
}

// TestConcentratorPerPeerQuotaIsolatesCrossPeerFlood is the D49 acceptance (b): an insider
// holding peer A's psk that floods k>=quota spoofed sources exhausts only ITS OWN per-peer quota
// (A refused past its quota) and never starves peer B's bootstrap — B's FIRST authenticated PROBE
// still binds.
func TestConcentratorPerPeerQuotaIsolatesCrossPeerFlood(t *testing.T) {
	pskA := testKey(t, 0x11) // the insider's own peer (primary)
	pskB := testKey(t, 0x22) // the victim whose bootstrap PROBE must survive
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	const capLimit = 4
	m.maxDemuxSources = capLimit
	const quota = capLimit / 2 // len(peers) == 2 (primary + peer B)
	secondView := peerPathByName(second, "a")

	// Insider flood: k > quota authenticated PROBEs under pskA from distinct spoofed sources.
	const k = 20
	for i := 1; i <= k; i++ {
		m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, uint64(i), clk), synthSource(i))
	}
	// A is refused past its quota: its footprint is clamped at the quota, never the whole cap.
	if got := m.peerBindingCountForTest(primary); got != quota {
		t.Fatalf("insider peer A occupies %d demux slots, want it clamped at its per-peer quota %d", got, quota)
	}

	// Peer B's FIRST authenticated PROBE from a fresh source still binds — the flood never
	// consumed B's headroom (Q27(1) isolation).
	freshB := synthSource(9000)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), freshB)
	if bound, ok := m.lookupPeerBySource(freshB); !ok || bound != second {
		t.Fatalf("peer B's bootstrap PROBE was starved by peer A's flood: bound=%v ok=%v", bound, ok)
	}
	if got := m.peerBySourceLenForTest(); got > capLimit {
		t.Fatalf("demux map %d exceeded the global cap %d", got, capLimit)
	}
}

// TestConcentratorSamePeerRoamChurnEvictsOwnOldest is the D49 acceptance (c) + the PINNED
// same-peer roam-churn decision: a single LIVE peer that authenticates MORE than its quota of
// distinct AddrPorts (CGNAT port churn) keeps binding — its OWN oldest binding is evicted (LRU
// within the peer), it is NEVER dropped, and NO other peer's slot is disturbed. TearDownPeer
// refuses the live peer, so LRU self-eviction is the only path by which it can re-bind past its
// quota.
func TestConcentratorSamePeerRoamChurnEvictsOwnOldest(t *testing.T) {
	pskA := testKey(t, 0x11) // an unrelated peer whose slot must stay put (the primary)
	pskB := testKey(t, 0x22) // the live, roaming peer under test
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	const capLimit = 4
	m.maxDemuxSources = capLimit
	const quota = capLimit / 2 // len(peers) == 2
	secondView := peerPathByName(second, "a")
	dataCodecB, _ := frame.NewCodec(pskB)

	// An unrelated primary binding that peer B's churn must never evict.
	srcA := synthSource(0)
	m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), srcA)

	// distinct AddrPorts sharing ONE CGNAT address, different ports (roam port churn).
	churn := func(seq uint64) netip.AddrPort {
		return netip.AddrPortFrom(netip.MustParseAddr("203.0.113.9"), uint16(7000+seq))
	}

	// Make peer B LIVE: bind its first source, drive its path Up. TearDownPeer must now refuse it,
	// so its stale port-churn bindings can never be reclaimed by teardown — LRU is the only path.
	first := churn(1)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), first)
	driveConcentratorPathUp(t, secondView, pskB, clk)
	if m.TearDownPeer("peer-b") {
		t.Fatal("TearDownPeer tore down the LIVE roaming peer — LRU self-eviction must be the reclaim path, not teardown")
	}

	// Roam churn: authenticate quota+extra distinct AddrPorts for the SAME peer B.
	ports := []netip.AddrPort{first}
	for seq := uint64(2); seq <= uint64(quota)+2; seq++ {
		ap := churn(seq)
		ports = append(ports, ap)
		m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, seq, clk), ap)
		// Peer B is NEVER dropped: the just-authenticated AddrPort always binds to B.
		if bound, ok := m.lookupPeerBySource(ap); !ok || bound != second {
			t.Fatalf("roam AddrPort %v was dropped instead of binding to the live peer: bound=%v ok=%v", ap, bound, ok)
		}
		// B's footprint never exceeds its quota (own-oldest is LRU-evicted to admit the new one).
		if got := m.peerBindingCountForTest(second); got > quota {
			t.Fatalf("peer B grew to %d bindings, past its quota %d (LRU self-eviction failed)", got, quota)
		}
	}

	// Exactly the quota MOST-RECENT AddrPorts remain bound; the older ones were LRU-evicted.
	if got := m.peerBindingCountForTest(second); got != quota {
		t.Fatalf("peer B holds %d bindings after churn, want exactly its quota %d", got, quota)
	}
	n := len(ports)
	for i, ap := range ports {
		_, ok := m.lookupPeerBySource(ap)
		wantBound := i >= n-quota // only the newest `quota` survive
		if ok != wantBound {
			t.Fatalf("churn port %v bound=%v, want %v (LRU keeps the newest %d)", ap, ok, wantBound, quota)
		}
	}
	// The unrelated primary binding was never disturbed by peer B's churn (cross-peer isolation).
	if bound, ok := m.lookupPeerBySource(srcA); !ok || bound != primary {
		t.Fatal("peer B's roam churn evicted the primary's binding — a peer must never evict ANOTHER peer's slot")
	}
	// DATA from the newest re-bound AddrPort reaches peer B's resequencer.
	newest := ports[n-1]
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 100, secondView.id, "roamed"), newest)
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("roamed")) {
		t.Fatalf("DATA from the re-bound roamed AddrPort was not delivered to peer B: ok=%v payload=%q", ok, it.Payload)
	}
}

// TestConcentratorCrossPeerDropOnGlobalExhaustion covers the RETAINED drop-on-exhaustion path
// (the bindSourceToPeer return-false branch): when the floor-1 per-peer quotas oversubscribe the
// GLOBAL cap, a peer BELOW its own quota whose new source would grow the map past the cap is
// REFUSED — it may not evict another peer's binding to make room (cross-peer isolation).
func TestConcentratorCrossPeerDropOnGlobalExhaustion(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	pskC := testKey(t, 0x33)
	pskD := testKey(t, 0x44)
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	third := bindLazyPeer(t, m, "peer-c", pskC, clk)
	fourth := bindLazyPeer(t, m, "peer-d", pskD, clk)
	// 4 peers, cap 3 → per-peer quota = max(1, 3/4) = 1; the quotas (sum 4) oversubscribe the cap.
	m.maxDemuxSources = 3
	secondView := peerPathByName(second, "a")
	thirdView := peerPathByName(third, "a")
	fourthView := peerPathByName(fourth, "a")

	// Fill the global cap: one source each for A, B, C (quota 1 apiece).
	srcA := synthSource(1)
	srcB := synthSource(2)
	srcC := synthSource(3)
	m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), srcA)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), srcB)
	m.demuxInbound(m.paths[0], authProbe(t, pskC, thirdView.id, 1, clk), srcC)
	if got := m.peerBySourceLenForTest(); got != 3 {
		t.Fatalf("demux map = %d, want the global cap 3 filled by A/B/C", got)
	}

	// Peer D is BELOW its quota (0 < 1) but the global cap is full: its first PROBE is DROPPED. It
	// cannot evict A/B/C's slots to make room.
	srcD := synthSource(4)
	m.demuxInbound(m.paths[0], authProbe(t, pskD, fourthView.id, 1, clk), srcD)
	if _, ok := m.lookupPeerBySource(srcD); ok {
		t.Fatal("peer D bound past the exhausted GLOBAL cap (drop-on-exhaustion violated)")
	}
	if got := m.peerBySourceLenForTest(); got != 3 {
		t.Fatalf("demux map grew past the cap to %d after a cross-peer exhausted arrival", got)
	}
	// A, B, C's bindings all survived — peer D never evicted another peer.
	if b, ok := m.lookupPeerBySource(srcA); !ok || b != primary {
		t.Fatal("peer A's binding was evicted by peer D's arrival")
	}
	if b, ok := m.lookupPeerBySource(srcB); !ok || b != second {
		t.Fatal("peer B's binding was evicted by peer D's arrival")
	}
	if b, ok := m.lookupPeerBySource(srcC); !ok || b != third {
		t.Fatal("peer C's binding was evicted by peer D's arrival")
	}
}
