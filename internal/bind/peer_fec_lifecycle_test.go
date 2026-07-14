package bind

import (
	"bytes"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newProbingMultipathFEC is newProbingMultipath with a FEC config threaded through NewMultipath,
// so a concentrator peer bound lazily over its sockets re-instantiates its FULL heavy trio
// (resequencer + FEC DECODE and FEC ENCODE planes) on binding — the FEC-on lifecycle the T91
// FEC-off fixtures could not exercise (every prior fixture passed nil fecCfg, making the
// 'fecRecv nil / fecSend nil' assertions vacuous).
func newProbingMultipathFEC(t testing.TB, paths []config.Path, psk config.Key, fecCfg *fec.Config, clk telemetry.Clock) (*Multipath, []*telemetry.Prober) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8) *telemetry.Prober {
		return telemetry.NewProber(name, id, testProbeSessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, len(paths))
	health := make([]sched.PathHealth, len(paths))
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i))
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Hour}, clk, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	m, err := NewMultipath(paths, psk, scheduler, probers, newProber, fecCfg, nil, config.Amnezia{})
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	return m, probers
}

// lazyConcentratorFEC is lazyConcentrator with FEC configured: a one-shared-socket Multipath
// whose primary is keyed by pskA and whose SECOND peer (pskB) is bound LAZILY (no heavy state
// yet). Because fecCfg is non-nil, ensurePeerReceiveInstantiated builds the peer's FEC decode
// AND encode planes on binding (and teardownPeerLocked frees them), so the FEC-plane lifecycle
// asserted below is driven by PRODUCTION code, not the fixture.
func lazyConcentratorFEC(t *testing.T, pskA, pskB config.Key, fecCfg *fec.Config) (m *Multipath, primary, second *peerState, clk *fakeClock) {
	t.Helper()
	clk = newFakeClock()
	m, _ = newProbingMultipathFEC(t, loopbackPaths(1), pskA, fecCfg, clk)
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

// offerReconstructingParity delivers, through the demux, a GENUINE single-frame-group parity
// (DataCount=1) from a source ALREADY BOUND to peer p, so it reaches p's FEC decoder via
// dispatchInbound's PARITY case. Offered alone, that parity reconstructs its sole data shard,
// so the decoder's Recovered counter must advance by one — the observable that a re-instantiated
// FEC decode plane is not merely present but actually recovering. It returns the decoder's
// Recovered delta.
func offerReconstructingParity(t *testing.T, m *Multipath, view *peerPathState, codec *frame.Codec, src netip.AddrPort, cfg fec.Config, seq uint64, inner string) uint64 {
	t.Helper()
	fr := view.peer.fecRecv.Load()
	if fr == nil {
		t.Fatal("offerReconstructingParity: peer has no FEC decoder armed")
	}
	before := fr.stats().Recovered
	group, index, dataCount, parityPayload := singleFrameParity(t, cfg, seq, []byte(inner))
	parity, err := codec.Encode(nil, frame.Parity{FECGroup: group, ParityIndex: index, DataCount: dataCount, PathID: view.id, Payload: parityPayload})
	if err != nil {
		t.Fatalf("encode reconstructing parity: %v", err)
	}
	m.demuxInbound(m.paths[0], parity, src)
	return fr.stats().Recovered - before
}

// TestConcentratorFECReceivePlaneLifecycle is the FEC-ENABLED T91 receive-plane acceptance
// (round-2 hardening): a configured concentrator peer's FEC DECODER is ABSENT before its first
// authenticated binding, INSTANTIATED on that binding and actually RECONSTRUCTING parity, FREED
// on teardown, and RE-INSTANTIATED as a FRESH decoder that reconstructs again after re-bind.
// Unlike the FEC-off fixtures, the 'fecRecv nil before / present after' assertions here are NOT
// vacuous: fecCfg is non-nil, so deleting ensurePeerReceiveInstantiated's fecRecv install leaves
// fecRecv nil after binding and this test red (mutation-verified).
func TestConcentratorFECReceivePlaneLifecycle(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	fecCfg := &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
	m, _, second, clk := lazyConcentratorFEC(t, pskA, pskB, fecCfg)
	secondView := peerPathByName(second, "a")
	codecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B codec: %v", err)
	}

	// (1) FEC decoder absent before the first authenticated binding.
	if second.fecRecv.Load() != nil {
		t.Fatal("a configured concentrator peer holds a FEC decoder before any binding (FEC receive not lazy)")
	}

	// (2) Binding instantiates the FEC decoder, and it actually reconstructs a parity-only group.
	src := synthSource(1)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), src)
	fr := second.fecRecv.Load()
	if fr == nil {
		t.Fatal("peer B's FEC decoder was not instantiated on its first authenticated binding")
	}
	if got := offerReconstructingParity(t, m, secondView, codecB, src, *fecCfg, 1000, "recovered-1"); got != 1 {
		t.Fatalf("freshly-instantiated FEC decoder recovered %d frames from a DataCount=1 parity, want 1", got)
	}

	// (3) Teardown frees the FEC decoder.
	if m.peerIsLiveLocked(second) {
		t.Fatal("peer B is unexpectedly live before any echo was fed")
	}
	if !m.TearDownPeer("peer-b") {
		t.Fatal("TearDownPeer refused to tear down a DEAD peer")
	}
	if second.fecRecv.Load() != nil {
		t.Fatal("teardown did not free peer B's FEC decoder")
	}

	// (4) Re-bind re-instantiates a FRESH decoder that reconstructs again.
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 2, clk), src)
	fr2 := second.fecRecv.Load()
	if fr2 == nil {
		t.Fatal("peer B's FEC decoder was not re-instantiated on re-bind")
	}
	if fr2 == fr {
		t.Fatal("re-bind reused the torn-down FEC decoder instance instead of a fresh one")
	}
	if got := offerReconstructingParity(t, m, secondView, codecB, src, *fecCfg, 2000, "recovered-2"); got != 1 {
		t.Fatalf("re-instantiated FEC decoder recovered %d frames after re-bind, want 1", got)
	}
}

// TestConcentratorFECSendPlaneReinstantiatedOnRebind is the round-2 acceptance for the PRODUCTION
// DEFECT the panel found in T91's own 're-instantiate + pass traffic on re-bind' criterion: a
// torn-down concentrator peer's FEC SEND plane (fecSend) was freed on teardown but NEVER rebuilt
// on re-bind (ensurePeerReceiveInstantiated was receive-only), so a rebound FEC-configured peer
// SENT WITHOUT PARITY until a full Close/Open. This asserts fecSend is absent → present on binding
// → freed on teardown → RE-INSTANTIATED (fresh) on re-bind, and that the rebound peer's Send
// actually EMITS PARITY again. Deleting the fecSend re-instantiation leaves this red.
func TestConcentratorFECSendPlaneReinstantiatedOnRebind(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	// DataShards=4: one Send batch of 4 inner datagrams closes exactly one group, emitting parity.
	fecCfg := &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
	m, _, second, clk := lazyConcentratorFEC(t, pskA, pskB, fecCfg)
	secondView := peerPathByName(second, "a")

	// (1) FEC send plane absent before binding.
	if second.fecSend.Load() != nil {
		t.Fatal("a configured concentrator peer holds a FEC sender before any binding")
	}

	// (2) Binding instantiates the FEC send plane.
	src := synthSource(1)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), src)
	fs := second.fecSend.Load()
	if fs == nil {
		t.Fatal("peer B's FEC sender was not instantiated on its first authenticated binding")
	}

	// (3) Teardown frees the FEC send plane (dead peer: no echoes fed).
	if !m.TearDownPeer("peer-b") {
		t.Fatal("TearDownPeer refused to tear down a DEAD peer")
	}
	if second.fecSend.Load() != nil {
		t.Fatal("teardown did not free peer B's FEC sender")
	}

	// (4) Re-bind re-instantiates a FRESH FEC send plane — the fix for the production defect.
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 2, clk), src)
	fs2 := second.fecSend.Load()
	if fs2 == nil {
		t.Fatal("re-bind did not re-instantiate peer B's FEC sender: a rebound FEC peer would send WITHOUT parity")
	}
	if fs2 == fs {
		t.Fatal("re-bind reused the torn-down FEC sender instance instead of a fresh one")
	}

	// (5) The rebound peer's Send actually EMITS PARITY again. Bring its path Up and give it a
	// remote, then Send one full group's worth of inner datagrams; the encoder must close the
	// group and egress parity, counted on fecSend.parityFrames.
	raw, rawAP := rawPeer(t)
	_ = raw
	secondView.setRemote(rawAP)
	driveConcentratorPathUp(t, secondView, pskB, clk)

	batch := [][]byte{[]byte("g0-0"), []byte("g0-1"), []byte("g0-2"), []byte("g0-3")}
	if err := m.Send(batch, second.virt); err != nil {
		t.Fatalf("Send through rebound peer B: %v", err)
	}
	if got := second.fecSend.Load().parityFrames.Load(); got == 0 {
		t.Fatal("rebound FEC peer emitted NO parity frames on a full group: fecSend was not truly re-instantiated (production defect)")
	}
}

// TestDispatchInboundNilGuardsDropNotPanic is the deterministic white-box acceptance for the
// dispatchInbound nil-guards (multipath.go DATA case + PARITY case). It reproduces the exact
// TearDownPeer-vs-readLoop ordering the guards defend: bindSourceToPeer has already resolved a
// still-bound source to peer B (so demuxInbound routes straight to dispatchInbound WITHOUT
// re-checking the ring), and TearDownPeer Store(nil)s peer B's resequencer out from under the
// in-flight dispatch. The guards must DROP the frame rather than dereference the nil ring. Each
// subtest FAILS (panics on a nil-pointer dereference) when its guard is removed — the prior round's
// guard tests were vacuous because their post-teardown source was already UNBOUND, so the frame
// never reached dispatchInbound. FEC is ON so the PARITY guard (which lives inside the fecRecv!=nil
// branch) is actually exercised.
func TestDispatchInboundNilGuardsDropNotPanic(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	fecCfg := &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}

	t.Run("DATA to a bound peer whose resequencer was niled mid-flight is dropped without panic", func(t *testing.T) {
		m, primary, second, clk := lazyConcentratorFEC(t, pskA, pskB, fecCfg)
		secondView := peerPathByName(second, "a")
		dataCodecB, err := frame.NewCodec(pskB)
		if err != nil {
			t.Fatalf("build peer B data codec: %v", err)
		}
		src := synthSource(1)
		m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), src)
		if bound, ok := m.lookupPeerBySource(src.Addr()); !ok || bound != second {
			t.Fatalf("probe did not bind source to peer B: bound=%v ok=%v", bound, ok)
		}
		// The source stays BOUND; only the ring is torn out — the ordering the DATA guard defends.
		second.resequencer.Store(nil)
		// Must not panic: with FEC armed, removing `rq == nil { return }` reaches rq.Observe on the
		// nil ring inside the fecRecv branch and panics.
		m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 100, secondView.id, "raced-data"), src)
		// The frame was dropped, never misrouted into the primary.
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("raced DATA leaked into the primary ring (%q)", it.Payload)
		}
	})

	t.Run("PARITY to a bound peer with FEC armed but resequencer niled mid-flight is dropped without panic", func(t *testing.T) {
		m, _, second, clk := lazyConcentratorFEC(t, pskA, pskB, fecCfg)
		secondView := peerPathByName(second, "a")
		codecB, err := frame.NewCodec(pskB)
		if err != nil {
			t.Fatalf("build peer B codec: %v", err)
		}
		src := synthSource(1)
		m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 1, clk), src)
		if second.fecRecv.Load() == nil {
			t.Fatal("peer B's FEC decoder was not armed after binding")
		}
		// Nil the ring but LEAVE fecRecv armed: the PARITY guard lives inside the fecRecv!=nil
		// branch. A DataCount=1 parity reconstructs its sole shard, so removing `rq == nil { return }`
		// drives observeRecovered -> rq.ObserveRecovered on the nil ring and panics.
		second.resequencer.Store(nil)
		group, index, dataCount, parityPayload := singleFrameParity(t, *fecCfg, 55, []byte("raced-parity"))
		parity, err := codecB.Encode(nil, frame.Parity{FECGroup: group, ParityIndex: index, DataCount: dataCount, PathID: secondView.id, Payload: parityPayload})
		if err != nil {
			t.Fatalf("encode parity: %v", err)
		}
		// Must not panic: the guard drops the reconstruction rather than resequencing on a nil ring.
		m.demuxInbound(m.paths[0], parity, src)
	})
}

// TestConcentratorTeardownRebindDemuxRace drives the CONCURRENT ordering the dispatchInbound
// nil-guards and the per-peer lifecycleMu exist for: the peer's SINGLE per-path readLoop resolves
// a still-bound source and dispatches DATA/PARITY (and re-binds, re-instantiating the planes) WHILE
// TearDownPeer — driven from the device's session-event goroutine — Store(nil)s those planes. It is
// exactly one demux driver against one teardown driver, matching production's one-readLoop-per-path
// discipline (a frame.Codec is single-goroutine by design; two concurrent demuxes on one path would
// be a test artifact, not a real race). Under -race it proves (a) no data race across the atomic
// trio nor the lifecycleMu-ordered (re)instantiation-vs-teardown, and (b) no nil-dereference panic —
// the DATA case's `rq == nil` guard and the PARITY case's guard drop a frame whose ring was niled
// mid-flight rather than dereferencing it. FEC is on so BOTH dispatch guards are live.
func TestConcentratorTeardownRebindDemuxRace(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	fecCfg := &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
	m, _, second, clk := lazyConcentratorFEC(t, pskA, pskB, fecCfg)
	secondView := peerPathByName(second, "a")
	codecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B codec: %v", err)
	}
	dataCodecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B data codec: %v", err)
	}
	src := synthSource(1)

	const rounds = 400
	for i := 0; i < rounds; i++ {
		// Precompute every wire serially in the test goroutine (the fake clock and the stateful
		// codecs are NOT concurrency-safe), so the demux goroutine below only touches the
		// concurrent targets under test: m.demuxInbound (a single per-path reader) and, on the
		// other goroutine, m.TearDownPeer.
		probeWire := authProbe(t, pskB, secondView.id, uint64(2*i+2), clk)
		dataWire := mustEncodeData(t, dataCodecB, uint64(1000+i), secondView.id, "d")
		group, index, dataCount, parityPayload := singleFrameParity(t, *fecCfg, uint64(5000+i), []byte("p"))
		parityWire, perr := codecB.Encode(nil, frame.Parity{FECGroup: group, ParityIndex: index, DataCount: dataCount, PathID: secondView.id, Payload: parityPayload})
		if perr != nil {
			t.Fatalf("encode parity: %v", perr)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		// The one per-path reader: re-bind (ensurePeerReceiveInstantiated), then dispatch DATA and
		// PARITY through the two nil-guards, all on ONE goroutine as a real readLoop would.
		go func() {
			defer wg.Done()
			m.demuxInbound(m.paths[0], probeWire, src)
			m.demuxInbound(m.paths[0], dataWire, src)
			m.demuxInbound(m.paths[0], parityWire, src)
		}()
		// The session-event driver: teardown — Store(nil)s the heavy trio under m.mu+lifecycleMu,
		// racing the reader's dispatch and re-instantiation.
		go func() {
			defer wg.Done()
			m.TearDownPeer("peer-b")
		}()
		wg.Wait()
	}

	// Positive postcondition: after all the churn the peer still re-binds cleanly and carries
	// DATA — the guards drop raced frames, they do not wedge the datapath.
	m.TearDownPeer("peer-b") // ensure a known torn-down starting point (no-op if already down)
	m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 999999, clk), src)
	if second.resequencer.Load() == nil {
		t.Fatal("peer B did not re-instantiate its ring after the concurrent churn")
	}
	m.demuxInbound(m.paths[0], mustEncodeData(t, dataCodecB, 888888, secondView.id, "final"), src)
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("final")) {
		t.Fatalf("peer B did not carry DATA after the concurrent churn: ok=%v payload=%q", ok, it.Payload)
	}
}
