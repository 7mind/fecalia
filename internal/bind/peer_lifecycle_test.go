package bind

import (
	"bytes"
	"crypto/rand"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// bindLazyPeer binds a SECOND concentrator peer over the shared sockets exactly as
// bindSecondPeer does, then clears its heavy receive datapath so the peer starts from the
// genuine LAZY baseline T91 pins: a configured concentrator peer carries NO ~2048-frame
// resequencer ring and NO FEC decoder buffers until its FIRST authenticated source->peer
// binding. bindSecondPeer predates lazy instantiation and eagerly stored a resequencer; we
// drop it here so every state transition asserted below is driven by PRODUCTION code
// (ensurePeerReceiveInstantiated on bind, teardownPeerLocked on session loss), not by the
// fixture. Its LIGHT state — psk-derived codecs, per-(peer,path) views, probers, reflector —
// survives, which is exactly what lets a trial-decode still authenticate the dormant peer.
func bindLazyPeer(t *testing.T, m *Multipath, name string, psk config.Key, clk telemetry.Clock) *peerState {
	t.Helper()
	p := bindSecondPeer(t, m, name, psk, clk)
	p.resequencer.Store(nil)
	p.fecRecv.Store(nil)
	return p
}

// lazyConcentrator stands up a one-shared-socket Multipath whose primary is keyed by pskA and
// whose SECOND peer (pskB) is bound LAZILY (no heavy state yet). It returns the bind, both
// peers, and the fake clock so a caller can drive per-path liveness deterministically.
func lazyConcentrator(t *testing.T, pskA, pskB config.Key) (m *Multipath, primary, second *peerState, clk *fakeClock) {
	t.Helper()
	clk = newFakeClock()
	m, _, _ = newProbingMultipath(t, loopbackPaths(1), pskA, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	primary = m.peerState
	second = bindLazyPeer(t, m, "peer-b", pskB, clk)
	if peerPathByName(second, "a") == nil {
		t.Fatalf("second peer has no view of shared path 'a': %v", pathNamesOfPeer(second))
	}
	return m, primary, second, clk
}

// synthSource returns a distinct spoofable source address for the i-th flood entry. The demux
// map is keyed by netip.Addr, so the ADDRESS (not just the port) must differ to occupy a
// distinct map slot.
func synthSource(i int) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 41000)
}

// authProbe encodes an authenticated PROBE under psk on the given path, from a caller-chosen
// ProbeSeq (kept strictly increasing across a flood so the reflector's per-path anti-replay
// filter never rejects — though binding happens BEFORE reflection and does not depend on it).
func authProbe(t *testing.T, psk config.Key, pathID uint8, seq uint64, clk *fakeClock) []byte {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{PathID: pathID, ProbeSeq: seq, TimestampNanos: clk.Now().UnixNano(), IsEcho: false})
	if err != nil {
		t.Fatalf("encode probe: %v", err)
	}
	return raw
}

// driveConcentratorPathUp brings a per-(peer,path) prober to StateUp through testProbeUpSucc
// authenticated probe/echo exchanges reflected under psk, advancing the fake clock by one
// probe interval per exchange — the same pattern the sweep tests use.
func driveConcentratorPathUp(t *testing.T, pp *peerPathState, psk config.Key, clk *fakeClock) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	for i := 0; i < testProbeUpSucc; i++ {
		raw, err := pp.prober.SendProbe()
		if err != nil {
			t.Fatalf("SendProbe: %v", err)
		}
		echo, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect probe: %v", err)
		}
		clk.advance(testProbeRTT)
		if err := pp.prober.HandleEcho(echo); err != nil {
			t.Fatalf("HandleEcho: %v", err)
		}
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if pp.prober.State() != telemetry.StateUp {
		t.Fatalf("path %q not Up after %d successes: %v", pp.name, testProbeUpSucc, pp.prober.State())
	}
}

// TestConcentratorLazyInstantiationTeardownRebind is the T91 per-peer-lifecycle acceptance: a
// configured concentrator peer's HEAVY receive datapath (the resequencer ring + FEC buffers)
// is ABSENT before its first authenticated binding, INSTANTIATED on that binding, TORN DOWN
// after session/liveness loss (freeing the ring), and RE-INSTANTIATED + passing traffic on the
// next authenticated PROBE.
func TestConcentratorLazyInstantiationTeardownRebind(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // lazy second peer
	m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
	secondView := peerPathByName(second, "a")
	dataCodecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B data codec: %v", err)
	}

	// (1) Heavy fields absent before the first authenticated binding.
	if second.resequencer.Load() != nil {
		t.Fatal("a configured concentrator peer holds a resequencer ring before any binding (heavy state not lazy)")
	}
	if second.fecRecv.Load() != nil {
		t.Fatal("a configured concentrator peer holds FEC receive buffers before any binding")
	}

	// (2) An authenticated PROBE from a fresh source binds it to peer B and INSTANTIATES the
	// heavy receive datapath.
	src := synthSource(1)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), src)
	bound, ok := m.lookupPeerBySource(src.Addr())
	if !ok || bound != second {
		t.Fatalf("authenticated PROBE did not bind the source to peer B: bound=%v ok=%v", bound, ok)
	}
	rq := second.resequencer.Load()
	if rq == nil {
		t.Fatal("peer B's resequencer ring was not instantiated on its first authenticated binding")
	}

	// Traffic flows: DATA from the now-bound source lands in peer B's freshly-built ring, and
	// only B's (never the primary's).
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 100, secondView.id, "hello-b"), src)
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("hello-b")) {
		t.Fatalf("post-bind DATA did not reach peer B's ring: ok=%v payload=%q", ok, it.Payload)
	}
	if it, ok := primary.resequencer.Load().Pop(); ok {
		t.Fatalf("peer B's DATA leaked into the primary ring (%q)", it.Payload)
	}

	// (3) Session/liveness loss: the peer is DOWN (no probe echoes fed), so TearDownPeer frees
	// its ring + FEC buffers and releases its source binding.
	if m.peerIsLiveLocked(second) {
		t.Fatal("peer B is unexpectedly live before any echo was fed")
	}
	if !m.TearDownPeer("peer-b") {
		t.Fatal("TearDownPeer refused to tear down a DEAD peer")
	}
	if second.resequencer.Load() != nil {
		t.Fatal("teardown did not free peer B's resequencer ring")
	}
	if second.fecRecv.Load() != nil {
		t.Fatal("teardown did not free peer B's FEC receive buffers")
	}
	if _, ok := m.lookupPeerBySource(src.Addr()); ok {
		t.Fatal("teardown did not release peer B's source->peer binding")
	}

	// DATA from the (now unbound) source is dropped — no ring, no binding, never misrouted.
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 101, secondView.id, "after-teardown"), src)
	if primary.resequencer.Load() == nil {
		t.Fatal("primary ring vanished (teardown must never touch another peer)")
	}
	if it, ok := primary.resequencer.Load().Pop(); ok {
		t.Fatalf("post-teardown DATA for peer B leaked into the primary ring (%q)", it.Payload)
	}

	// (4) Re-bind: a fresh authenticated PROBE re-instantiates the ring cleanly, and traffic
	// flows again.
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 2, clk), src)
	rebound, ok := m.lookupPeerBySource(src.Addr())
	if !ok || rebound != second {
		t.Fatalf("re-bind PROBE did not re-bind the source to peer B: bound=%v ok=%v", rebound, ok)
	}
	if second.resequencer.Load() == nil {
		t.Fatal("peer B's ring was not re-instantiated on re-bind")
	}
	if second.resequencer.Load() == rq {
		t.Fatal("re-bind reused the torn-down resequencer instance instead of a fresh one")
	}
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 200, secondView.id, "hello-again"), src)
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("hello-again")) {
		t.Fatalf("re-bound peer B did not pass traffic: ok=%v payload=%q", ok, it.Payload)
	}
}

// TestConcentratorDemuxCapBoundsBootstrapFlood is the T91 DoS-bound acceptance: a flood of many
// distinct spoofed unbound source addresses cannot grow the demux state past the configured cap
// and cannot evict or disturb a live bound peer.
func TestConcentratorDemuxCapBoundsBootstrapFlood(t *testing.T) {
	pskA := testKey(t, 0x11) // primary (the live peer under test)
	pskB := testKey(t, 0x22) // second peer, the flood's nominal target psk

	t.Run("a flood of spoofed unbound sources binds nothing and disturbs no live bound peer", func(t *testing.T) {
		m, primary, _, clk := lazyConcentrator(t, pskA, pskB)
		dataCodecA, err := frame.NewCodec(pskA)
		if err != nil {
			t.Fatalf("build primary data codec: %v", err)
		}

		// Establish a LIVE bound peer: bind a source to the primary via an authenticated PROBE,
		// drive its path Up, and leave a DATA frame buffered in its ring.
		live := synthSource(0)
		m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), live)
		if bound, ok := m.lookupPeerBySource(live.Addr()); !ok || bound != primary {
			t.Fatalf("live source did not bind to the primary: bound=%v ok=%v", bound, ok)
		}
		driveConcentratorPathUp(t, m.paths[0], pskA, clk)
		m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecA, 500, m.paths[0].id, "live-payload"), live)

		before := m.peerBySourceLenForTest()

		// Flood: 500 distinct spoofed sources sending random garbage that verifies under NO psk.
		const flood = 500
		for i := 1; i <= flood; i++ {
			garbage := make([]byte, 96)
			if _, err := rand.Read(garbage); err != nil {
				t.Fatalf("draw garbage: %v", err)
			}
			m.demuxInbound(m.paths[0], garbage, synthSource(i))
		}

		// The flood grew the demux map by nothing (spoofed garbage never authenticates a PROBE,
		// so it never binds), never evicted the live binding, and never disturbed its ring or
		// its liveness.
		if after := m.peerBySourceLenForTest(); after != before {
			t.Fatalf("spoofed flood grew the demux map from %d to %d entries", before, after)
		}
		if bound, ok := m.lookupPeerBySource(live.Addr()); !ok || bound != primary {
			t.Fatal("the flood evicted or repointed the live source->peer binding")
		}
		if m.paths[0].prober.State() != telemetry.StateUp {
			t.Fatalf("the flood disturbed the live path's liveness: %v", m.paths[0].prober.State())
		}
		if it, ok := primary.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("live-payload")) {
			t.Fatalf("the flood disturbed the live peer's buffered frame: ok=%v payload=%q", ok, it.Payload)
		}
	})

	t.Run("authenticated bindings clamp at the cap and never evict an existing binding", func(t *testing.T) {
		m, primary, second, clk := lazyConcentrator(t, pskA, pskB)
		const capLimit = 4
		m.maxDemuxSources = capLimit
		secondView := peerPathByName(second, "a")

		// The first bound source belongs to the PRIMARY (a DIFFERENT peer than the flood target),
		// so the cap test also proves cross-peer non-disturbance.
		live := synthSource(0)
		m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), live)
		if bound, ok := m.lookupPeerBySource(live.Addr()); !ok || bound != primary {
			t.Fatalf("live source did not bind to the primary: bound=%v ok=%v", bound, ok)
		}

		// Now flood authenticated PROBEs under pskB from many distinct sources — each WOULD bind
		// to peer B. Only up to the cap can be admitted; the rest are dropped-on-exhaustion.
		for i := 1; i <= 50; i++ {
			m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, uint64(i+1), clk), synthSource(i))
		}

		if got := m.peerBySourceLenForTest(); got != capLimit {
			t.Fatalf("demux map = %d entries, want it clamped at the cap %d", got, capLimit)
		}
		// The pre-existing binding was never evicted to make room for a newer source.
		if bound, ok := m.lookupPeerBySource(live.Addr()); !ok || bound != primary {
			t.Fatal("cap exhaustion evicted the pre-existing (primary) binding — drop-on-exhaustion must never evict")
		}
		// A brand-new source arriving while at cap is refused (bootstrap degrades, no growth).
		fresh := synthSource(9999)
		m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 9999, clk), fresh)
		if _, ok := m.lookupPeerBySource(fresh.Addr()); ok {
			t.Fatal("a new source bound while the demux map was at its cap (drop-on-exhaustion violated)")
		}
		if got := m.peerBySourceLenForTest(); got != capLimit {
			t.Fatalf("demux map grew past the cap to %d after a post-exhaustion arrival", got)
		}
	})
}

// TestConcentratorLivePeerNeverTornDown pins the invariant that a LIVE (Up) peer is never torn
// down regardless of other peers' churn: TearDownPeer refuses it while its path is Up, and a
// dormant peer's bind+teardown churn leaves the live peer's ring and binding untouched.
func TestConcentratorLivePeerNeverTornDown(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // the LIVE peer under test
	pskC := testKey(t, 0x33) // a dormant peer that churns

	m, _, second, clk := lazyConcentrator(t, pskA, pskB)
	secondView := peerPathByName(second, "a")
	third := bindLazyPeer(t, m, "peer-c", pskC, clk)
	thirdView := peerPathByName(third, "a")
	dataCodecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B data codec: %v", err)
	}

	// Make peer B live: bind a source, instantiate its ring, drive its path Up, buffer a frame.
	srcB := synthSource(1)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), srcB)
	driveConcentratorPathUp(t, secondView, pskB, clk)
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 700, secondView.id, "live-b"), srcB)
	ringB := second.resequencer.Load()
	if ringB == nil {
		t.Fatal("peer B has no ring after binding")
	}

	// TearDownPeer REFUSES the live peer.
	if m.TearDownPeer("peer-b") {
		t.Fatal("TearDownPeer tore down a LIVE (Up) peer")
	}
	if second.resequencer.Load() != ringB {
		t.Fatal("a refused teardown still swapped/freed the live peer's ring")
	}

	// Churn a DORMANT peer: bind peer C (Down), then tear it down. Peer B (live) must be
	// wholly untouched by that churn.
	srcC := synthSource(2)
	m.demuxInbound(m.paths[0], authProbe(t, pskC, thirdView.id, 1, clk), srcC)
	if third.resequencer.Load() == nil {
		t.Fatal("dormant peer C did not instantiate on binding")
	}
	if !m.TearDownPeer("peer-c") {
		t.Fatal("TearDownPeer refused to tear down the dead peer C")
	}
	if third.resequencer.Load() != nil {
		t.Fatal("peer C teardown did not free its ring")
	}

	// Peer B survived the churn: still Up, still bound, ring intact with its buffered frame.
	if secondView.prober.State() != telemetry.StateUp {
		t.Fatalf("peer B lost liveness across peer C's churn: %v", secondView.prober.State())
	}
	if bound, ok := m.lookupPeerBySource(srcB.Addr()); !ok || bound != second {
		t.Fatal("peer B's binding was disturbed by peer C's churn")
	}
	if second.resequencer.Load() != ringB {
		t.Fatal("peer B's ring was swapped by peer C's churn")
	}
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("live-b")) {
		t.Fatalf("peer B's buffered frame was disturbed by peer C's churn: ok=%v payload=%q", ok, it.Payload)
	}
}

// peerBySourceLenForTest reports the number of live source->peer demux bindings (0 when the
// map has never been published).
func (m *Multipath) peerBySourceLenForTest() int {
	mp := m.peerBySource.Load()
	if mp == nil {
		return 0
	}
	return len(*mp)
}

// mustEncodeData encodes a DATA frame or fails the test.
func mustEncodeData(t *testing.T, codec *frame.Codec, seq uint64, pathID uint8, payload string) []byte {
	t.Helper()
	raw, err := codec.Encode(nil, frame.Data{OuterSeq: seq, PathID: pathID, Payload: []byte(payload)})
	if err != nil {
		t.Fatalf("encode DATA: %v", err)
	}
	return raw
}
